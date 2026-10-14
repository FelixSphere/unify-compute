package admin_test

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// `admin topup refund` creates the refund at Stripe and nothing else; the
// balance moves only when charge.refunded reaches the webhook (task B2).

type refundEnv struct {
	*env
	stripe *stripetest.Server
	pay    *payments.Service
}

func newRefundEnv(t *testing.T) *refundEnv {
	e := newEnv(t)
	fake := stripetest.New(t)
	pay := payments.New(payments.Config{DB: e.db, Accounts: e.acc, Billing: e.bill, Stripe: fake.Client(),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: payments.Settings{SecretKey: stripetest.Key, WebhookSecret: stripetest.Secret, Environment: "staging"}})
	return &refundEnv{env: e, stripe: fake, pay: pay}
}

func (e *refundEnv) refund(args ...string) error {
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Payments: e.pay, Actor: "ops-alice", Out: &e.out}
	return cli.Run(ctx, append([]string{"topup", "refund"}, args...))
}

// paid writes a credited top-up and credits it, as the webhook would.
func (e *refundEnv) paid(account int64, cents int, pi string) string {
	e.t.Helper()
	id := store.NewID("tu_", time.Now())
	if _, err := e.db.Exec(ctx, `INSERT INTO topups (id, account_id, user_id, amount_cents, status, stripe_checkout_session_id,
		stripe_payment_intent_id, created_at, paid_at) VALUES ($1, $2, 1, $3, 'credited', $4, $5, now(), now())`,
		id, account, cents, "cs_"+pi, pi); err != nil {
		e.t.Fatal(err)
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.bill.CreditTopupTx(ctx, tx, account, int64(cents)*5000, id); err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *refundEnv) webhook(evt string, obj any) string {
	e.t.Helper()
	body := stripetest.Event(evt, "charge.refunded", false, obj)
	out, err := e.pay.HandleWebhook(ctx, payments.Sign(body, stripetest.Secret, time.Now()), body)
	e.pay.Wait()
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestTopupRefundIsIssuedAtStripeAndAppliedByTheWebhook(t *testing.T) {
	e := newRefundEnv(t)
	acct := e.signup("pat@example.com", true)
	tu := e.paid(acct, 2000, "pi_admin1")

	if err := e.refund(tu, "--usd", "5", "--reason", "customer asked, ticket 12"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "USD 5.00") || !strings.Contains(e.out.String(), "re_fake") {
		t.Fatalf("output %q", e.out.String())
	}
	reqs := e.stripe.Requests()
	r := reqs[len(reqs)-1]
	if r.Path != "/v1/refunds" || r.Form.Get("payment_intent") != "pi_admin1" || r.Form.Get("amount") != "500" ||
		r.Form.Get("metadata[product]") != "unify-compute" || r.Form.Get("metadata[topup_id]") != tu || r.IdempotencyKey == "" {
		t.Fatalf("refund request %+v", r)
	}
	// Nothing moved yet: the webhook applies it.
	if b, _ := e.bill.Balance(ctx, acct); b.Available != 2000*5000 {
		t.Fatalf("CLI moved the balance: %d", b.Available)
	}
	// Run twice before the webhook lands: Stripe sees the same idempotency
	// key, so one refund.
	if err := e.refund(tu, "--amount", "5.00", "--reason", "retry"); err != nil {
		t.Fatal(err)
	}
	reqs = e.stripe.Requests()
	if reqs[len(reqs)-1].IdempotencyKey != r.IdempotencyKey {
		t.Fatal("a repeated refund used a new idempotency key")
	}

	if out := e.webhook("evt_admin_r1", stripetest.ChargeObject("pi_admin1", 2000, 500)); out != payments.OutcomeRefunded {
		t.Fatalf("webhook: %s", out)
	}
	if b, _ := e.bill.Balance(ctx, acct); b.Available != 1500*5000 {
		t.Fatalf("available after refund %d", b.Available)
	}
	// The rest, with no amount: everything left on the top-up.
	if err := e.refund(tu, "--reason", "close account"); err != nil {
		t.Fatal(err)
	}
	reqs = e.stripe.Requests()
	if got := reqs[len(reqs)-1].Form.Get("amount"); got != "1500" {
		t.Fatalf("refund of the rest asked for %s", got)
	}
	var stages []string
	for _, a := range e.audits(acct) {
		if a.Action == "refund" {
			stages = append(stages, a.ActorType+":"+a.Detail["stage"].(string))
		}
	}
	if got := strings.Join(stages, ","); got != "admin:requested,admin:requested,stripe:applied,admin:requested" {
		t.Fatalf("refund audit %s", got)
	}
}

func TestTopupRefundRefusals(t *testing.T) {
	e := newRefundEnv(t)
	acct := e.signup("pat@example.com", true)
	tu := e.paid(acct, 1000, "pi_admin2")
	before := len(e.stripe.Requests())

	// USD 7 of the 10 spent on GPUs: only 3 can go back.
	if _, err := e.db.Exec(ctx, `UPDATE accounts SET available = 1500000 WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `INSERT INTO balance_entries (id, account_id, kind, available_delta, held_delta, available_after, held_after, ref_type, ref_id, idempotency_key)
		VALUES ($1, $2, 'admin_debit', -3500000, 0, 1500000, 0, 'test', 'spent', 'test:spent')`, store.NewID("be_", time.Now()), acct); err != nil {
		t.Fatal(err)
	}
	var re *payments.ErrRefund
	if err := e.refund(tu, "--usd", "5", "--reason", "x"); !errors.As(err, &re) || !strings.Contains(err.Error(), "compute already used") {
		t.Fatalf("above available: %v", err)
	}
	if err := e.refund(tu, "--usd", "10.01", "--reason", "x"); !errors.As(err, &re) {
		t.Fatalf("above the top-up: %v", err)
	}
	if err := e.refund(tu, "--usd", "1"); !errors.Is(err, admin.ErrUsage) {
		t.Fatalf("no reason: %v", err)
	}
	if err := e.refund(tu, "--usd", "1.005", "--reason", "x"); !errors.Is(err, admin.ErrUsage) {
		t.Fatalf("three decimals: %v", err)
	}
	if err := e.refund("123", "--reason", "x"); !errors.Is(err, admin.ErrUsage) {
		t.Fatalf("not a top-up id: %v", err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE topups SET status = 'disputed' WHERE id = $1`, tu); err != nil {
		t.Fatal(err)
	}
	if err := e.refund(tu, "--usd", "1", "--reason", "x"); !errors.As(err, &re) || !strings.Contains(err.Error(), "disputed") {
		t.Fatalf("disputed top-up: %v", err)
	}
	if n := len(e.stripe.Requests()); n != before {
		t.Fatalf("a refused refund reached Stripe (%d requests)", n-before)
	}
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Actor: "ops", Out: &e.out}
	if err := cli.Run(ctx, []string{"topup", "refund", tu, "--reason", "x"}); err == nil || errors.Is(err, admin.ErrUsage) {
		t.Fatalf("without payments: %v", err)
	}
}
