package payments

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
)

// Webhook outcomes, stored in stripe_events.outcome and answered in the
// response body so the Stripe dashboard's delivery log says what happened.
const (
	OutcomeCredited        = "credited"
	OutcomeAlreadyCredited = "already_credited"
	OutcomeExpired         = "expired"
	OutcomeFailed          = "failed"
	OutcomeAwaitingPayment = "awaiting_payment"
	OutcomeNoChange        = "no_change"
	// OutcomeRecorded: a refund or dispute on a compute top-up, stored but
	// not applied until B2 builds the flow. Logged as an error for the
	// operator.
	OutcomeRecorded = "recorded"
	// OutcomeRejected: compute's own event that does not match its top-up
	// (amount, currency, account, session). Never credited; logged as an
	// error, answered 200 so Stripe does not retry something that will
	// never become valid.
	OutcomeRejected  = "rejected"
	OutcomeDuplicate = "duplicate"
	// OutcomeIgnored: not compute's (another product on the shared Stripe
	// account, another environment, the other mode, or a type compute does
	// not handle). Nothing is stored.
	OutcomeIgnored = "ignored"
)

// MaxWebhookBody bounds the raw body read (spec/accounts.md, "Stripe webhook").
const MaxWebhookBody = 256 << 10

type event struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Livemode bool   `json:"livemode"`
	Data     struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// chargeRef is the part of a Charge or Dispute object that ties it to a
// payment: compute recognises its own by the PaymentIntent of a top-up.
type chargeRef struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Charge        string            `json:"charge"`
	PaymentIntent string            `json:"payment_intent"`
	Amount        int64             `json:"amount"`
	Status        string            `json:"status"`
	Metadata      map[string]string `json:"metadata"`
}

// HandleWebhook verifies and applies one delivery. A signature error is
// ErrNoSignature, ErrBadSignature or ErrStaleSignature (400, nothing
// stored); any other error is ours (500, Stripe retries for three days).
func (s *Service) HandleWebhook(ctx context.Context, signature string, body []byte) (string, error) {
	if s.whsec == "" {
		return "", ErrUnavailable
	}
	if err := VerifySignature(signature, body, s.whsec, s.now()); err != nil {
		return "", err
	}
	var ev event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		// Signed by Stripe yet unreadable: retrying will not help.
		s.log.Error("stripe webhook: signed body is not an event", "err", err)
		return OutcomeIgnored, nil
	}
	log := s.log.With("event", ev.ID, "type", ev.Type)
	if ev.Livemode != s.livemode {
		log.Warn("stripe webhook: event from the other mode ignored", "livemode", ev.Livemode)
		return OutcomeIgnored, nil
	}

	var apply func(tx pgx.Tx) (outcome, topupID string, err error)
	var credited *creditDone
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded",
		"checkout.session.async_payment_failed", "checkout.session.expired":
		var cs CheckoutSession
		if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
			log.Error("stripe webhook: unreadable checkout session", "err", err)
			return OutcomeIgnored, nil
		}
		if cs.Metadata["product"] != Product || cs.Metadata["environment"] != s.env {
			return OutcomeIgnored, nil
		}
		apply = func(tx pgx.Tx) (string, string, error) {
			out, done, err := s.applySession(ctx, tx, ev, cs, log)
			credited = done
			return out, cs.ClientReferenceID, err
		}
	case "charge.refunded", "charge.dispute.created", "charge.dispute.closed":
		var ref chargeRef
		if err := json.Unmarshal(ev.Data.Object, &ref); err != nil {
			log.Error("stripe webhook: unreadable charge or dispute", "err", err)
			return OutcomeIgnored, nil
		}
		id, err := s.topupForPayment(ctx, ref)
		if err != nil {
			return "", err
		}
		if id == "" {
			return OutcomeIgnored, nil
		}
		apply = func(pgx.Tx) (string, string, error) {
			// B2 builds refunds and disputes. Until then the event is kept
			// so B2 can replay it, and the operator is told.
			log.Error("stripe webhook: refund or dispute on a top-up recorded but NOT applied (B2 not built); operator action needed",
				"topup", id, "object", ref.ID, "amount", ref.Amount, "status", ref.Status)
			return OutcomeRecorded, id, nil
		}
	default:
		return OutcomeIgnored, nil
	}

	sum := sha256.Sum256(body)
	var outcome string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		// Inserted first: a concurrent redelivery waits on this row and then
		// finds it, so an event is applied at most once.
		tag, err := tx.Exec(ctx, `INSERT INTO stripe_events (event_id, type, livemode, outcome, payload_sha256, received_at, processed_at)
			VALUES ($1, $2, $3, 'processing', $4, $5, $5) ON CONFLICT (event_id) DO NOTHING`, ev.ID, ev.Type, ev.Livemode, sum[:], now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			outcome = OutcomeDuplicate
			return nil
		}
		out, topupID, err := apply(tx)
		if err != nil {
			return err
		}
		outcome = out
		var tid *string
		if topupID != "" {
			tid = &topupID
		}
		_, err = tx.Exec(ctx, `UPDATE stripe_events SET outcome = $2, topup_id = $3, processed_at = $4 WHERE event_id = $1`,
			ev.ID, out, tid, s.now())
		return err
	})
	if err != nil {
		return "", err
	}
	if outcome == OutcomeDuplicate {
		credited = nil
	}
	if credited != nil {
		log.Info("top-up credited", "topup", credited.topupID, "account", credited.accountID)
		s.afterCredit(credited)
	}
	return outcome, nil
}

type creditDone struct {
	topupID       string
	accountID     int64
	paymentIntent string
	cents         int64
	// available is the account's available balance right after the credit,
	// in quota units.
	available int64
	paidAt    time.Time
}

func (s *Service) applySession(ctx context.Context, tx pgx.Tx, ev event, cs CheckoutSession, log *slog.Logger) (string, *creditDone, error) {
	var t struct {
		accountID int64
		cents     int64
		status    string
		session   *string
	}
	err := tx.QueryRow(ctx, `SELECT account_id, amount_cents, status, stripe_checkout_session_id FROM topups WHERE id = $1 FOR UPDATE`,
		cs.ClientReferenceID).Scan(&t.accountID, &t.cents, &t.status, &t.session)
	if errors.Is(err, pgx.ErrNoRows) {
		log.Error("stripe webhook: compute checkout session names no known top-up", "client_reference_id", cs.ClientReferenceID, "session", cs.ID)
		return OutcomeRejected, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	// Everything must agree with what compute created; a mismatch is never
	// credited, whatever Stripe says was paid.
	switch {
	case t.session == nil || *t.session != cs.ID,
		cs.Metadata["topup_id"] != cs.ClientReferenceID,
		cs.Metadata["account_id"] != strconv.FormatInt(t.accountID, 10),
		cs.Mode != "payment", cs.Currency != "usd", cs.AmountTotal != t.cents:
		log.Error("stripe webhook: checkout session does not match its top-up; not credited", "topup", cs.ClientReferenceID,
			"session", cs.ID, "mode", cs.Mode, "currency", cs.Currency, "amount_total", cs.AmountTotal, "amount_cents", t.cents,
			"metadata_account", cs.Metadata["account_id"], "account", t.accountID)
		return OutcomeRejected, nil, nil
	}

	switch ev.Type {
	case "checkout.session.expired":
		return s.close(ctx, tx, cs.ClientReferenceID, t.status, StatusExpired, OutcomeExpired)
	case "checkout.session.async_payment_failed":
		return s.close(ctx, tx, cs.ClientReferenceID, t.status, StatusFailed, OutcomeFailed)
	case "checkout.session.completed":
		if cs.PaymentStatus != "paid" {
			// A delayed payment method; async_payment_succeeded or _failed
			// follows. Cards only, so this should not happen.
			return OutcomeAwaitingPayment, nil, nil
		}
	}
	// completed and paid, or async_payment_succeeded.
	switch t.status {
	case StatusOpen:
	case StatusExpired, StatusFailed:
		log.Error("stripe webhook: payment for a top-up that is no longer open; not credited, operator action needed",
			"topup", cs.ClientReferenceID, "status", t.status, "payment_intent", cs.PaymentIntent)
		return OutcomeRejected, nil, nil
	default:
		return OutcomeAlreadyCredited, nil, nil
	}
	bal, err := s.bill.CreditTopupTx(ctx, tx, t.accountID, t.cents*quotaPerCent, cs.ClientReferenceID)
	if err != nil {
		return "", nil, err
	}
	var pi *string
	if cs.PaymentIntent != "" {
		pi = &cs.PaymentIntent
	}
	paidAt := s.now()
	if _, err := tx.Exec(ctx, `UPDATE topups SET status = 'credited', paid_at = $2, stripe_payment_intent_id = $3 WHERE id = $1`,
		cs.ClientReferenceID, paidAt, pi); err != nil {
		return "", nil, err
	}
	if _, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorStripe, ActorID: ev.ID, AccountID: t.accountID,
		Action: accounts.ActionTopupCredited, Detail: map[string]any{"topup_id": cs.ClientReferenceID,
			"amount_usd": CentsUSD(int(t.cents)), "checkout_session": cs.ID, "available_after_usd": billing.USD(bal.Available)}}); err != nil {
		return "", nil, err
	}
	return OutcomeCredited, &creditDone{topupID: cs.ClientReferenceID, accountID: t.accountID, paymentIntent: cs.PaymentIntent,
		cents: t.cents, available: bal.Available, paidAt: paidAt}, nil
}

// close ends an open top-up without payment. Anything but open is left as
// it is: an expiry never undoes a credit.
func (s *Service) close(ctx context.Context, tx pgx.Tx, id, from, to, outcome string) (string, *creditDone, error) {
	if from != StatusOpen {
		return OutcomeNoChange, nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE topups SET status = $2 WHERE id = $1 AND status = 'open'`, id, to); err != nil {
		return "", nil, err
	}
	return outcome, nil, nil
}

// topupForPayment finds the top-up a charge or dispute belongs to, by its
// PaymentIntent, or "" when it is another product's.
func (s *Service) topupForPayment(ctx context.Context, ref chargeRef) (string, error) {
	if ref.PaymentIntent == "" {
		return "", nil
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM topups WHERE stripe_payment_intent_id = $1`, ref.PaymentIntent).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// afterCredit runs after the commit, in the background, and never undoes
// the credit: it stores Stripe's receipt link, which a Checkout Session does
// not carry, then emails "Top-up received" with that link if it was found.
// The receipt lookup comes first so the email can carry it; the email goes
// out without it when the lookup fails.
func (s *Service) afterCredit(d *creditDone) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		receipt := s.fetchReceipt(d)
		s.mailReceipt(d, receipt)
	}()
}

// fetchReceipt stores the charge id and Stripe's hosted receipt URL and
// returns the URL, or "" when there is none.
func (s *Service) fetchReceipt(d *creditDone) string {
	if d.paymentIntent == "" || s.stripe == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	charge, receipt, err := s.stripe.Receipt(ctx, d.paymentIntent)
	if err != nil {
		s.log.Warn("stripe: receipt lookup failed; the top-up stays credited without a receipt link", "topup", d.topupID, "err", err)
		return ""
	}
	if _, err := s.db.Exec(ctx, `UPDATE topups SET stripe_charge_id = NULLIF($2, ''), receipt_url = NULLIF($3, '') WHERE id = $1`,
		d.topupID, charge, receipt); err != nil {
		s.log.Error("store receipt", "topup", d.topupID, "err", err)
	}
	return receipt
}

// mailReceipt sends "Top-up received" to the account's owners, once per
// top-up however many events credit-adjacent deliveries bring (notify). A
// failure is logged by notify and changes nothing here.
func (s *Service) mailReceipt(d *creditDone, receipt string) {
	if s.mail == nil || s.notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r := mailer.TopupReceipt{ID: d.topupID, AmountUSD: CentsUSD(int(d.cents)), BalanceUSD: billing.USD(d.available),
		PaidAt: d.paidAt, ReceiptURL: receipt}
	_, _ = s.notifier.ToOwners(ctx, notify.Key{AccountID: d.accountID, Subject: "topup", SubjectID: d.topupID, Message: mailer.KindTopupReceived},
		func(ctx context.Context, to string) error { return s.mail.TopupReceived(ctx, to, r) })
}
