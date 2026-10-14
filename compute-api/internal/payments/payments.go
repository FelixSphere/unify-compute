// Package payments is Stripe Checkout top-ups (ADR-0011 §4, docs/spec/
// accounts.md "Top-up" and "Stripe webhook"; task B1).
//
// The dashboard creates a top-up; compute-api creates a Checkout Session for
// it and the browser goes to Stripe's hosted page. Only the webhook credits
// a balance, once per top-up, in one transaction with the top-up's state,
// its stripe_events row, a balance entry of kind topup and an audit row. The
// page the customer returns to never credits anything.
//
// The Stripe account is shared with Unify API (same company, its own
// production integration). Compute uses its own restricted keys and its own
// webhook endpoint, tags everything it creates with metadata.product =
// unify-compute, the account id and the environment, and answers every
// event that is not its own with 200 and does nothing with it, so Stripe
// neither retries it nor reports the endpoint as failing. Nothing here is
// taken from the console's payment code (AGPL, ADR-0002); it follows
// Stripe's public API reference.
package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Product is metadata.product on every Stripe object compute creates, and
// the first thing the webhook checks on a Checkout event.
const Product = "unify-compute"

const (
	// CheckoutTTL is how long a Checkout Session can be paid (Stripe allows
	// 30 minutes to 24 hours). Short, because a payment link left in an old
	// tab is a liability; creating a new top-up expires the old one anyway.
	CheckoutTTL = time.Hour
	// quotaPerCent: 500000 quota units per USD 1 (spec/accounts.md, Units).
	quotaPerCent = sku.QuotaPerUSD / 100
	// orphanAge: a top-up row still without a session id after this long is
	// left from a request that died between the insert and Stripe's answer.
	// The customer never saw a payment link for it, so it is deleted.
	orphanAge = time.Minute
	maxListed = 50
	stripeTO  = 20 * time.Second
)

// Top-up statuses (spec/accounts.md, "Top-up").
const (
	StatusOpen     = "open"
	StatusCredited = "credited"
	StatusExpired  = "expired"
	StatusFailed   = "failed"
)

// Error is a refusal the dashboard maps onto the error envelope as is.
type Error struct {
	Status  int
	Type    string
	Code    string
	Message string
	Param   string
	// TopupID names the open top-up on 409 topup_open.
	TopupID string
}

func (e *Error) Error() string { return "payments: " + e.Code }

const contact = "contactus@felixsphere.com"

var (
	ErrUnavailable = &Error{Status: http.StatusServiceUnavailable, Type: "topups_unavailable", Code: "topups_unavailable",
		Message: "Card top-ups are not available right now. Contact us at " + contact + " and we'll add credit by hand."}
	ErrInvalidAmount = &Error{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_amount",
		Message: "Enter an amount in US dollars, like 20 or 20.00.", Param: "amount_usd"}
	ErrAccountSuspended = &Error{Status: http.StatusForbidden, Type: "authentication_error", Code: "account_suspended",
		Message: "This account is suspended, so it cannot be topped up. Contact us at " + contact + "."}
	ErrStripeUnavailable = &Error{Status: http.StatusBadGateway, Type: "payment_provider_error", Code: "stripe_unavailable",
		Message: "We couldn't reach our payment provider. Nothing was charged. Try again in a minute."}
	ErrNotFound = &Error{Status: http.StatusNotFound, Type: "not_found", Code: "not_found", Message: "Not found."}
)

func limitError(msg string, param string) *Error {
	return &Error{Status: http.StatusForbidden, Type: "permission_error", Code: "topup_limit_exceeded", Message: msg, Param: param}
}

// Settings are the Stripe keys and the environment they are used in.
type Settings struct {
	SecretKey     string // STRIPE_SECRET_KEY
	WebhookSecret string // STRIPE_WEBHOOK_SECRET
	Environment   string // ENVIRONMENT
}

func SettingsFromEnv(getenv func(string) string) Settings {
	return Settings{SecretKey: strings.TrimSpace(getenv("STRIPE_SECRET_KEY")),
		WebhookSecret: strings.TrimSpace(getenv("STRIPE_WEBHOOK_SECRET")), Environment: getenv("ENVIRONMENT")}
}

// Configured reports whether either key is set. Neither is the normal state
// of a deployment without top-ups, not an error; one alone is (Check).
func (s Settings) Configured() bool { return s.SecretKey != "" || s.WebhookSecret != "" }

// Livemode is whether the key moves real money.
func (s Settings) Livemode() bool {
	return strings.HasPrefix(s.SecretKey, "sk_live_") || strings.HasPrefix(s.SecretKey, "rk_live_")
}

// Check refuses keys that must not be used here. A live key anywhere but
// ENVIRONMENT=production would take real money on staging; a test key in
// production would hand out credit for payments nobody made. Both keys come
// together, since a key without its webhook secret creates sessions that
// can never be credited.
func (s Settings) Check() error {
	switch {
	case s.SecretKey == "" || s.WebhookSecret == "":
		return errors.New("STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET must be set together")
	case !regexp.MustCompile(`^(sk|rk)_(test|live)_[0-9A-Za-z]+$`).MatchString(s.SecretKey):
		return errors.New("STRIPE_SECRET_KEY is not a Stripe secret or restricted key (sk_… or rk_…)")
	case !strings.HasPrefix(s.WebhookSecret, "whsec_"):
		return errors.New("STRIPE_WEBHOOK_SECRET is not a webhook signing secret (whsec_…)")
	case s.Livemode() && s.Environment != "production":
		return fmt.Errorf("a live Stripe key is set with ENVIRONMENT=%q; live keys are for production only", s.Environment)
	case !s.Livemode() && s.Environment == "production":
		return errors.New("a test Stripe key is set with ENVIRONMENT=production")
	}
	return nil
}

type Config struct {
	DB       *pgxpool.Pool
	Accounts *accounts.Service
	Billing  *billing.Local
	// Stripe nil disables top-ups: creating one and the webhook answer 503,
	// listing still works.
	Stripe   *Stripe
	Settings Settings
	// AppOrigin is where Checkout sends the customer back (/billing).
	AppOrigin string
	Log       *slog.Logger
	Now       func() time.Time
	// Mailer and Notifier, both set, send the account's owners "Top-up
	// received" once per credited top-up. Either nil sends nothing.
	Mailer   *mailer.Mailer
	Notifier *notify.Notifier
}

type Service struct {
	db        *pgxpool.Pool
	acc       *accounts.Service
	bill      *billing.Local
	stripe    *Stripe
	whsec     string
	livemode  bool
	env       string
	appOrigin string
	log       *slog.Logger
	now       func() time.Time
	wg        sync.WaitGroup
	mail      *mailer.Mailer
	notifier  *notify.Notifier
}

func New(cfg Config) *Service {
	s := &Service{db: cfg.DB, acc: cfg.Accounts, bill: cfg.Billing, stripe: cfg.Stripe, whsec: cfg.Settings.WebhookSecret,
		livemode: cfg.Settings.Livemode(), env: cmpEnv(cfg.Settings.Environment), appOrigin: strings.TrimRight(cfg.AppOrigin, "/"),
		log: cfg.Log, now: cfg.Now, mail: cfg.Mailer, notifier: cfg.Notifier}
	if s.now == nil {
		s.now = time.Now
	}
	if s.stripe == nil {
		s.whsec = ""
	}
	return s
}

// cmpEnv is metadata.environment. Never empty: Stripe drops a metadata key
// set to "", and the webhook compares it with its own.
func cmpEnv(env string) string {
	if env == "" {
		return "local"
	}
	return env
}

// Enabled reports whether top-ups can be created.
func (s *Service) Enabled() bool { return s != nil && s.stripe != nil }

// Wait blocks until receipt lookups started after a webhook have finished.
func (s *Service) Wait() { s.wg.Wait() }

// Topup is a top-up as the dashboard shows it.
type Topup struct {
	ID            string
	AccountID     int64
	AmountCents   int
	Status        string
	SessionID     string
	ReceiptURL    string
	RefundedCents int
	CreatedAt     time.Time
	PaidAt        *time.Time
}

// Created is POST /topups' answer.
type Created struct {
	ID          string
	Status      string
	CheckoutURL string
}

var amountRe = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,2})?$`)

// ParseAmount reads a USD amount with at most two decimals into cents.
func ParseAmount(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if !amountRe.MatchString(s) {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	for len(frac) < 2 {
		frac += "0"
	}
	w, _ := strconv.Atoi(whole)
	f, _ := strconv.Atoi(frac)
	return w*100 + f, true
}

// CentsUSD renders cents as a two-place amount, "20.00".
func CentsUSD(c int) string { return new(big.Rat).SetFrac64(int64(c), 100).FloatString(2) }

type accountRow struct {
	status                             string
	available, held                    int64
	minCents, maxCents, capCents, perD int
	customer                           *string
}

func loadAccount(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64, lock bool) (accountRow, error) {
	var a accountRow
	sql := `SELECT status, available, held, topup_min_cents, topup_max_cents, balance_max_cents, topups_per_day, stripe_customer_id
		FROM accounts WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, id).Scan(&a.status, &a.available, &a.held, &a.minCents, &a.maxCents, &a.capCents, &a.perD, &a.customer)
	return a, err
}

// limits are ADR-0011 D4, from the account's own columns so the operator can
// raise one account's: the per-top-up range, the balance cap counted on
// available plus held, and top-ups per rolling 24 hours. Expired and failed
// top-ups do not count against the day: nothing was paid, and a customer
// who backs out of Checkout must be able to try again. Session creation is
// bounded separately (the dashboard's per-account throttle) and card
// testing inside one session by Stripe Radar.
func (s *Service) limits(ctx context.Context, tx pgx.Tx, accountID int64, a accountRow, cents int) error {
	if cents < a.minCents {
		return limitError("The minimum top-up is USD "+CentsUSD(a.minCents)+".", "amount_usd")
	}
	if cents > a.maxCents {
		return limitError("The most you can add at once is USD "+CentsUSD(a.maxCents)+". Contact us at "+contact+" if you need more.", "amount_usd")
	}
	balanceCents := (a.available + a.held + quotaPerCent - 1) / quotaPerCent
	if balanceCents+int64(cents) > int64(a.capCents) {
		room := int64(a.capCents) - balanceCents
		msg := "Your balance is at its limit of USD " + CentsUSD(a.capCents) + "."
		if room >= int64(a.minCents) {
			msg = "That would take your balance over USD " + CentsUSD(a.capCents) + ", the most an account can hold. You can add up to USD " + CentsUSD(int(room)) + " now."
		}
		return limitError(msg+" Contact us at "+contact+" if you need a higher limit.", "amount_usd")
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM topups WHERE account_id = $1 AND created_at > $2 AND status NOT IN ('expired', 'failed')`,
		accountID, s.now().Add(-24*time.Hour)).Scan(&n); err != nil {
		return err
	}
	if n >= a.perD {
		return limitError(fmt.Sprintf("You've made %d top-ups in the last 24 hours, the most allowed. Try again later, or contact us at %s.", a.perD, contact), "")
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func topupOpen(id string) *Error {
	return &Error{Status: http.StatusConflict, Type: "conflict", Code: "topup_open", TopupID: id,
		Message: "Your previous payment is still being processed. It will appear in your balance shortly; then you can add more."}
}

// CreateTopup checks the limits, records an open top-up, and creates its
// Checkout Session. The amount is in dollars as the page sends it.
func (s *Service) CreateTopup(ctx context.Context, u accounts.User, amountUSD string, c accounts.ClientInfo) (Created, error) {
	if !s.Enabled() {
		return Created{}, ErrUnavailable
	}
	cents, ok := ParseAmount(amountUSD)
	if !ok {
		return Created{}, ErrInvalidAmount
	}
	a, err := loadAccount(ctx, s.db, u.AccountID, false)
	if err != nil {
		return Created{}, err
	}
	if a.status != accounts.AccountActive {
		return Created{}, ErrAccountSuspended
	}
	// The cheap refusals first, before anything reaches Stripe; checked
	// again under the account lock below.
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return s.limits(ctx, tx, u.AccountID, a, cents) }); err != nil {
		return Created{}, err
	}
	if err := s.closeOpen(ctx, u.AccountID); err != nil {
		return Created{}, err
	}
	cust, err := s.customer(ctx, u, a)
	if err != nil {
		return Created{}, err
	}

	now := s.now()
	id := store.NewID("tu_", now)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		a, err := loadAccount(ctx, tx, u.AccountID, true)
		if err != nil {
			return err
		}
		if a.status != accounts.AccountActive {
			return ErrAccountSuspended
		}
		if err := s.limits(ctx, tx, u.AccountID, a, cents); err != nil {
			return err
		}
		var open string
		err = tx.QueryRow(ctx, `SELECT id FROM topups WHERE account_id = $1 AND status = 'open'`, u.AccountID).Scan(&open)
		if err == nil {
			return topupOpen(open)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO topups (id, account_id, user_id, amount_cents, stripe_customer_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, u.AccountID, u.ID, cents, cust, now)
		if isUniqueViolation(err) {
			return topupOpen("")
		}
		return err
	})
	if err != nil {
		return Created{}, err
	}

	meta := s.metadata(u.AccountID, id)
	form := url.Values{
		"mode":                                          {"payment"},
		"line_items[0][quantity]":                       {"1"},
		"line_items[0][price_data][currency]":           {"usd"},
		"line_items[0][price_data][unit_amount]":        {strconv.Itoa(cents)},
		"line_items[0][price_data][product_data][name]": {"Unify Compute credit"},
		"payment_method_types[0]":                       {"card"},
		"customer":                                      {cust},
		"client_reference_id":                           {id},
		"success_url":                                   {s.appOrigin + "/billing?topup=" + id + "&checkout=success"},
		"cancel_url":                                    {s.appOrigin + "/billing?topup=" + id + "&checkout=cancelled"},
		"expires_at":                                    {strconv.FormatInt(now.Add(CheckoutTTL).Unix(), 10)},
		"submit_type":                                   {"pay"},
		"saved_payment_method_options[payment_method_save]": {"disabled"},
		"payment_intent_data[description]":                  {"Unify Compute credit " + id},
	}
	putMetadata(form, "metadata", meta)
	putMetadata(form, "payment_intent_data[metadata]", meta)
	// price_data makes Stripe create an inline Product; tag it too, so
	// nothing compute causes in the shared account is untagged.
	putMetadata(form, "line_items[0][price_data][product_data][metadata]", map[string]string{"product": Product, "environment": s.env})
	sctx, cancel := context.WithTimeout(ctx, stripeTO)
	defer cancel()
	// The idempotency key is the top-up id: a retried request can never make
	// a second session, and so a second payment, for one top-up.
	cs, err := s.stripe.CreateCheckoutSession(sctx, form, "unify-compute:topup:"+id)
	if err == nil && (cs.ID == "" || cs.URL == "") {
		err = &StripeError{Message: "checkout session without id or url"}
	}
	if err != nil {
		s.log.Error("stripe: create checkout session", "topup", id, "account", u.AccountID, "err", err)
		if _, derr := s.db.Exec(context.WithoutCancel(ctx), `DELETE FROM topups WHERE id = $1 AND stripe_checkout_session_id IS NULL`, id); derr != nil {
			s.log.Error("delete top-up without a session", "topup", id, "err", derr)
		}
		return Created{}, ErrStripeUnavailable
	}
	expires := time.Unix(cs.ExpiresAt, 0)
	if cs.ExpiresAt == 0 {
		expires = now.Add(CheckoutTTL)
	}
	if _, err := s.db.Exec(ctx, `UPDATE topups SET stripe_checkout_session_id = $2, expires_at = $3 WHERE id = $1`,
		id, cs.ID, expires); err != nil {
		return Created{}, err
	}
	if _, err := s.acc.Audit(ctx, accounts.AuditEvent{ActorType: accounts.ActorUser, ActorID: strconv.FormatInt(u.ID, 10),
		AccountID: u.AccountID, Action: accounts.ActionTopupCreated, Client: c,
		Detail: map[string]any{"topup_id": id, "amount_usd": CentsUSD(cents), "checkout_session": cs.ID}}); err != nil {
		s.log.Error("audit write failed", "action", accounts.ActionTopupCreated, "topup", id, "err", err)
	}
	return Created{ID: id, Status: StatusOpen, CheckoutURL: cs.URL}, nil
}

func (s *Service) metadata(accountID int64, topupID string) map[string]string {
	m := map[string]string{"product": Product, "account_id": strconv.FormatInt(accountID, 10), "environment": s.env}
	if topupID != "" {
		m["topup_id"] = topupID
	}
	return m
}

// customer returns the account's Stripe Customer, creating it on the first
// top-up (one per account, so Radar sees one history per account).
func (s *Service) customer(ctx context.Context, u accounts.User, a accountRow) (string, error) {
	if a.customer != nil && *a.customer != "" {
		return *a.customer, nil
	}
	sctx, cancel := context.WithTimeout(ctx, stripeTO)
	defer cancel()
	// The environment is in the key: staging and a developer's machine share
	// test mode and both number accounts from 1.
	id, err := s.stripe.CreateCustomer(sctx, u.Email, s.metadata(u.AccountID, ""),
		"unify-compute:"+s.env+":customer:"+strconv.FormatInt(u.AccountID, 10))
	if err != nil {
		s.log.Error("stripe: create customer", "account", u.AccountID, "err", err)
		return "", ErrStripeUnavailable
	}
	// Two first top-ups at once share the idempotency key, so both got the
	// same customer; whichever writes second changes nothing.
	if _, err := s.db.Exec(ctx, `UPDATE accounts SET stripe_customer_id = $2 WHERE id = $1 AND stripe_customer_id IS NULL`, u.AccountID, id); err != nil {
		return "", err
	}
	return id, nil
}

// closeOpen makes room for a new top-up. An open top-up's Checkout Session
// is expired at Stripe first, so a payment page left open in another tab can
// no longer be paid. If Stripe says the session is already complete, the
// payment went through and its webhook will credit it: 409 topup_open.
func (s *Service) closeOpen(ctx context.Context, accountID int64) error {
	var id string
	var session *string
	var created time.Time
	err := s.db.QueryRow(ctx, `SELECT id, stripe_checkout_session_id, created_at FROM topups WHERE account_id = $1 AND status = 'open'`,
		accountID).Scan(&id, &session, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if session == nil {
		if s.now().Sub(created) < orphanAge {
			return topupOpen(id)
		}
		_, err := s.db.Exec(ctx, `DELETE FROM topups WHERE id = $1 AND stripe_checkout_session_id IS NULL`, id)
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, stripeTO)
	defer cancel()
	cs, err := s.stripe.ExpireCheckoutSession(sctx, *session)
	if err != nil {
		var se *StripeError
		if !errors.As(err, &se) || se.Status == 0 || se.Status >= 500 {
			s.log.Error("stripe: expire checkout session", "topup", id, "err", err)
			return ErrStripeUnavailable
		}
		// Refused: no longer open. Ask what it is instead.
		if cs, err = s.stripe.GetCheckoutSession(sctx, *session); err != nil {
			s.log.Error("stripe: get checkout session", "topup", id, "err", err)
			return ErrStripeUnavailable
		}
	}
	switch cs.Status {
	case "expired":
		_, err := s.db.Exec(ctx, `UPDATE topups SET status = 'expired' WHERE id = $1 AND status = 'open'`, id)
		return err
	case "complete":
		return topupOpen(id)
	default:
		s.log.Error("stripe: checkout session neither expired nor complete after expire", "topup", id, "status", cs.Status)
		return ErrStripeUnavailable
	}
}

const topupColumns = `id, account_id, amount_cents, status, COALESCE(stripe_checkout_session_id, ''), COALESCE(receipt_url, ''),
	refunded_cents, created_at, paid_at`

func scanTopup(r pgx.Row) (Topup, error) {
	var t Topup
	err := r.Scan(&t.ID, &t.AccountID, &t.AmountCents, &t.Status, &t.SessionID, &t.ReceiptURL, &t.RefundedCents, &t.CreatedAt, &t.PaidAt)
	return t, err
}

// ListTopups is the account's newest top-ups, at most 50 (three a day). A
// top-up whose session was never created is not shown: it is being created
// or about to be deleted.
func (s *Service) ListTopups(ctx context.Context, accountID int64) ([]Topup, error) {
	rows, err := s.db.Query(ctx, `SELECT `+topupColumns+` FROM topups
		WHERE account_id = $1 AND stripe_checkout_session_id IS NOT NULL ORDER BY created_at DESC, id DESC LIMIT $2`, accountID, maxListed)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Topup, error) { return scanTopup(r) })
}

// GetTopup is one top-up of the account; another account's is ErrNotFound.
func (s *Service) GetTopup(ctx context.Context, accountID int64, id string) (Topup, error) {
	t, err := scanTopup(s.db.QueryRow(ctx, `SELECT `+topupColumns+` FROM topups
		WHERE id = $1 AND account_id = $2 AND stripe_checkout_session_id IS NOT NULL`, id, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Topup{}, ErrNotFound
	}
	return t, err
}
