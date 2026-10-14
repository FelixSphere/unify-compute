package dashboard

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Refunds and disputes on top-ups, end to end through the signed webhook
// (task B2, spec/accounts.md "Top-up"). The cancel path itself is
// exercised against a real jobs.Service in internal/jobs.

const opsAddr = "ops@example.com"

type cancelCall struct {
	owner, id string
	at        time.Time
}

type fakeCanceller struct {
	mu    sync.Mutex
	calls []cancelCall
}

func (c *fakeCanceller) Cancel(_ context.Context, owner, id string, at time.Time) (store.Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, cancelCall{owner, id, at})
	return store.Job{}, nil
}

type refundFixture struct {
	*topupFixture
	jobs *fakeCanceller
}

func newRefundFixture(t *testing.T) *refundFixture {
	t.Helper()
	f := newTopupFixture(t, true)
	jc := &fakeCanceller{}
	f.pay = payments.New(payments.Config{DB: f.db, Accounts: f.acc, Billing: f.bill, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Stripe: f.stripe.Client(), Mailer: f.h.mail, OperatorEmail: opsAddr, Jobs: jc,
		Settings: payments.Settings{SecretKey: stripetest.Key, WebhookSecret: stripetest.Secret, Environment: "staging"}})
	h, err := New(Config{Accounts: f.acc, Mailer: f.h.mail, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Ledger: f.bill, Payments: f.pay})
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	return &refundFixture{topupFixture: f, jobs: jc}
}

// paidTopup creates and pays a top-up and returns its id and PaymentIntent.
func (f *refundFixture) paidTopup(s session, amount string) (string, string) {
	f.t.Helper()
	c := f.mustTopup(s, amount)
	if out := f.pay_(c.ID); out != payments.OutcomeCredited {
		f.t.Fatalf("pay: %s", out)
	}
	f.pay.Wait()
	var pi string
	if err := f.db.QueryRow(context.Background(), `SELECT stripe_payment_intent_id FROM topups WHERE id = $1`, c.ID).Scan(&pi); err != nil {
		f.t.Fatal(err)
	}
	return c.ID, pi
}

func (f *refundFixture) send(typ string, obj any) string {
	f.t.Helper()
	out := outcomeOf(f.t, f.signed(f.event(typ, obj)))
	f.pay.Wait()
	return out
}

func (f *refundFixture) topupRow(id string) (status string, refunded, disputed int) {
	f.t.Helper()
	if err := f.db.QueryRow(context.Background(), `SELECT status, refunded_cents, disputed_cents FROM topups WHERE id = $1`, id).
		Scan(&status, &refunded, &disputed); err != nil {
		f.t.Fatal(err)
	}
	return
}

func (f *refundFixture) accountState(id int64) (status, reason string) {
	f.t.Helper()
	if err := f.db.QueryRow(context.Background(), `SELECT status, coalesce(suspended_reason, '') FROM accounts WHERE id = $1`, id).
		Scan(&status, &reason); err != nil {
		f.t.Fatal(err)
	}
	return
}

func kinds(ms []sent) string {
	var k []string
	for _, m := range ms {
		k = append(k, m.kind)
	}
	return strings.Join(k, ",")
}

// insertJob puts a live job of the account straight into the store, as a
// submit would have left it.
func (f *refundFixture) insertJob(acct int64, status string) string {
	f.t.Helper()
	id := store.NewJobID(f.clock.now())
	if _, err := f.db.Exec(context.Background(), `INSERT INTO jobs (id, owner, sku, status, spec) VALUES ($1, $2, 'a10g-x1', $3, '{}')`,
		id, "account:"+itoa(acct), status); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestRefundPartialThenFullDebitsEachOnceWhateverIsRedelivered(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	id, pi := f.paidTopup(s, "20.00")
	before := len(f.mail.to("ada@example.com"))

	// A partial refund of USD 5.
	partial := f.event("charge.refunded", stripetest.ChargeObject(pi, 2000, 500))
	if out := outcomeOf(t, f.signed(partial)); out != payments.OutcomeRefunded {
		t.Fatalf("partial refund: %s", out)
	}
	f.pay.Wait()
	if got := f.available(s); got != "15.0000" {
		t.Fatalf("available after partial refund %s", got)
	}
	if st, r, _ := f.topupRow(id); st != payments.StatusPartiallyRefunded || r != 500 {
		t.Fatalf("top-up %s refunded %d", st, r)
	}

	// The same delivery again, and the same refund under a new event id
	// (Stripe sending charge.refunded twice for one refund): nothing moves,
	// and no second email.
	if out := outcomeOf(t, f.signed(partial)); out != payments.OutcomeDuplicate {
		t.Fatalf("redelivery: %s", out)
	}
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 2000, 500)); out != payments.OutcomeNoChange {
		t.Fatalf("same refund, new event: %s", out)
	}
	if got := f.available(s); got != "15.0000" {
		t.Fatalf("available after duplicates %s", got)
	}
	mails := f.mail.to("ada@example.com")[before:]
	if kinds(mails) != "refund-issued" || !strings.Contains(mails[0].text, "USD 5.00") ||
		!strings.Contains(mails[0].text, "Unused prepaid balance can be refunded on request") {
		t.Fatalf("refund emails %+v", mails)
	}

	// The rest: cumulative 2000, so 15 more.
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 2000, 2000)); out != payments.OutcomeRefunded {
		t.Fatalf("full refund: %s", out)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("available after full refund %s", got)
	}
	if st, r, _ := f.topupRow(id); st != payments.StatusRefunded || r != 2000 {
		t.Fatalf("top-up %s refunded %d", st, r)
	}
	// An older event arriving last (cumulative 500 after 2000) changes nothing.
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 2000, 500)); out != payments.OutcomeNoChange {
		t.Fatalf("stale event: %s", out)
	}
	if n := f.count(`SELECT count(*) FROM balance_entries WHERE kind = 'refund' AND ref_id = $1`, id); n != 2 {
		t.Fatalf("%d refund entries, want 2", n)
	}
	if n := f.count(`SELECT count(*) FROM payment_emails WHERE kind = 'refund_issued' AND sent_at IS NOT NULL`); n != 2 {
		t.Fatalf("%d refund emails sent, want 2", n)
	}
	if n := len(f.jobs.calls); n != 0 {
		t.Fatalf("a refund cancelled %d jobs", n)
	}
	f.balanced()
}

func TestRefundLargerThanAvailableGoesNegativeWithoutSuspending(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("bo@example.com")
	acct := f.accountOf("bo@example.com")
	_, pi := f.paidTopup(s, "10.00")
	job := f.insertJob(acct, "running")
	// Spent USD 8 of it: an operator debit stands in for settled usage.
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET available = available - 4000000 WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(context.Background(), `INSERT INTO balance_entries (id, account_id, kind, available_delta, held_delta, available_after, held_after, ref_type, ref_id, idempotency_key)
		VALUES ($1, $2, 'admin_debit', -4000000, 0, 1000000, 0, 'test', 'spent', 'test:spent')`, store.NewID("be_", f.clock.now()), acct); err != nil {
		t.Fatal(err)
	}
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 1000, 1000)); out != payments.OutcomeRefunded {
		t.Fatalf("refund: %s", out)
	}
	if got := f.available(s); got != "-8.0000" {
		t.Fatalf("available %s, want -8.0000", got)
	}
	if st, _ := f.accountState(acct); st != "active" {
		t.Fatalf("account %s; a refund does not suspend", st)
	}
	if n := f.count(`SELECT count(*) FROM job_cancel_requests WHERE job_id = $1`, job); n != 0 || len(f.jobs.calls) != 0 {
		t.Fatal("a refund cancelled a running job")
	}
	if kinds(f.mail.to(opsAddr)) != "operator-notice" {
		t.Fatalf("operator mail %+v", f.mail.to(opsAddr))
	}
	f.balanced()
}

func TestDisputeSuspendsCancelsAndAWonDisputeReinstates(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("cy@example.com")
	acct := f.accountOf("cy@example.com")
	id, pi := f.paidTopup(s, "30.00")
	running := f.insertJob(acct, "running")
	queued := f.insertJob(acct, "queued")
	done := f.insertJob(acct, "running")
	if _, err := f.db.Exec(context.Background(), `UPDATE jobs SET status = 'succeeded', finished_at = now() WHERE id = $1`, done); err != nil {
		t.Fatal(err)
	}
	before := len(f.mail.to("cy@example.com"))

	opened := f.event("charge.dispute.created", stripetest.DisputeObject("dp_1", pi, 3000, "needs_response"))
	if out := outcomeOf(t, f.signed(opened)); out != payments.OutcomeDisputed {
		t.Fatalf("dispute: %s", out)
	}
	f.pay.Wait()
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("available after dispute %s", got)
	}
	if st, why := f.accountState(acct); st != "suspended" || why != "dispute" {
		t.Fatalf("account %s (%s)", st, why)
	}
	if st, _, d := f.topupRow(id); st != payments.StatusDisputed || d != 3000 {
		t.Fatalf("top-up %s disputed %d", st, d)
	}
	// Both live jobs, and only they, are cancelled: durably, and through
	// the cancel path right away.
	if n := f.count(`SELECT count(*) FROM job_cancel_requests WHERE job_id = ANY($1)`, []string{running, queued}); n != 2 {
		t.Fatalf("%d cancel requests", n)
	}
	if n := f.count(`SELECT count(*) FROM job_cancel_requests WHERE job_id = $1`, done); n != 0 {
		t.Fatal("a finished job was cancelled")
	}
	if len(f.jobs.calls) != 2 || f.jobs.calls[0].owner != "account:"+itoa(acct) {
		t.Fatalf("cancel calls %+v", f.jobs.calls)
	}
	if k := kinds(f.mail.to("cy@example.com")[before:]); k != "account-suspended" {
		t.Fatalf("customer mail %s", k)
	}
	if k := kinds(f.mail.to(opsAddr)); k != "operator-notice" {
		t.Fatalf("operator mail %s", k)
	}
	// Suspended: no top-ups.
	wantErr(t, f.topup(s, "10"), 403, "authentication_error", "account_suspended")

	// Delivered again, nothing more happens.
	if out := outcomeOf(t, f.signed(opened)); out != payments.OutcomeDuplicate {
		t.Fatalf("redelivery: %s", out)
	}
	f.pay.Wait()
	if len(f.jobs.calls) != 2 || len(f.mail.to("cy@example.com")[before:]) != 1 {
		t.Fatal("a redelivered dispute acted twice")
	}

	// Won: the amount comes back and the suspension it caused is lifted.
	if out := f.send("charge.dispute.closed", stripetest.DisputeObject("dp_1", pi, 3000, "won")); out != payments.OutcomeDisputeWon {
		t.Fatalf("won: %s", out)
	}
	if got := f.available(s); got != "30.0000" {
		t.Fatalf("available after won %s", got)
	}
	if st, why := f.accountState(acct); st != "active" || why != "" {
		t.Fatalf("account after won %s (%s)", st, why)
	}
	if st, _, d := f.topupRow(id); st != payments.StatusCredited || d != 0 {
		t.Fatalf("top-up after won %s disputed %d", st, d)
	}
	// A second close changes nothing.
	if out := f.send("charge.dispute.closed", stripetest.DisputeObject("dp_1", pi, 3000, "won")); out != payments.OutcomeNoChange {
		t.Fatalf("second close: %s", out)
	}
	if n := f.count(`SELECT count(*) FROM balance_entries WHERE kind = 'dispute_reversal'`); n != 1 {
		t.Fatalf("%d reversals", n)
	}
	var actions []string
	rows, _ := f.db.Query(context.Background(), `SELECT action FROM audit_events WHERE account_id = $1 AND actor_type = 'stripe' ORDER BY seq`, acct)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	if got := strings.Join(actions, ","); got != "topup_credited,dispute_opened,account_suspended,account_reinstated,dispute_closed" {
		t.Fatalf("audit %s", got)
	}
	f.balanced()
}

func TestWonDisputeLeavesAnOperatorSuspension(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("dee@example.com")
	acct := f.accountOf("dee@example.com")
	_, pi := f.paidTopup(s, "10.00")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET status = 'suspended', suspended_reason = 'operator' WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	before := len(f.mail.to("dee@example.com"))
	if out := f.send("charge.dispute.created", stripetest.DisputeObject("dp_2", pi, 1000, "needs_response")); out != payments.OutcomeDisputed {
		t.Fatalf("dispute: %s", out)
	}
	if out := f.send("charge.dispute.closed", stripetest.DisputeObject("dp_2", pi, 1000, "won")); out != payments.OutcomeDisputeWon {
		t.Fatalf("won: %s", out)
	}
	if st, why := f.accountState(acct); st != "suspended" || why != "operator" {
		t.Fatalf("account %s (%s); the operator's suspension must stay", st, why)
	}
	if got := f.available(s); got != "10.0000" {
		t.Fatalf("available %s", got)
	}
	if n := len(f.mail.to("dee@example.com")[before:]); n != 0 {
		t.Fatalf("%d customer emails for an account already suspended", n)
	}
	f.balanced()
}

func TestLostDisputeStaysDebitedAndSuspended(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("eve@example.com")
	acct := f.accountOf("eve@example.com")
	id, pi := f.paidTopup(s, "10.00")
	// The close arrives first (Stripe does not promise order): a lost
	// dispute is applied in full on its own.
	if out := f.send("charge.dispute.closed", stripetest.DisputeObject("dp_3", pi, 1000, "lost")); out != payments.OutcomeDisputeLost {
		t.Fatalf("lost: %s", out)
	}
	if out := f.send("charge.dispute.created", stripetest.DisputeObject("dp_3", pi, 1000, "needs_response")); out != payments.OutcomeNoChange {
		t.Fatalf("late open: %s", out)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("available %s", got)
	}
	if st, why := f.accountState(acct); st != "suspended" || why != "dispute" {
		t.Fatalf("account %s (%s)", st, why)
	}
	if st, _, _ := f.topupRow(id); st != payments.StatusLost {
		t.Fatalf("top-up %s", st)
	}
	if n := f.count(`SELECT count(*) FROM balance_entries WHERE kind = 'dispute'`); n != 1 {
		t.Fatalf("%d dispute entries", n)
	}
	f.balanced()
}

func TestRefundEventThatDoesNotFitItsTopupIsRecordedNotApplied(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("fay@example.com")
	_, pi := f.paidTopup(s, "10.00")
	wrong := stripetest.ChargeObject(pi, 1000, 500)
	wrong["currency"] = "eur"
	if out := f.send("charge.refunded", wrong); out != payments.OutcomeRecorded {
		t.Fatalf("eur refund: %s", out)
	}
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 999, 500)); out != payments.OutcomeRecorded {
		t.Fatalf("amount mismatch: %s", out)
	}
	if got := f.available(s); got != "10.0000" {
		t.Fatalf("available %s", got)
	}
	f.balanced()
}

func TestEmailFailureIsRetriedOnRedeliveryAndSentOnce(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("gus@example.com")
	_, pi := f.paidTopup(s, "10.00")
	before := len(f.mail.to("gus@example.com"))
	body := f.event("charge.refunded", stripetest.ChargeObject(pi, 1000, 200))
	f.mail.fail.Store(true)
	if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeRefunded {
		t.Fatalf("refund: %s", out)
	}
	f.pay.Wait()
	f.mail.fail.Store(false)
	if n := len(f.mail.to("gus@example.com")[before:]); n != 0 {
		t.Fatalf("%d sent while failing", n)
	}
	for range 2 {
		if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeDuplicate {
			t.Fatalf("redelivery: %s", out)
		}
		f.pay.Wait()
	}
	if k := kinds(f.mail.to("gus@example.com")[before:]); k != "refund-issued" {
		t.Fatalf("after redelivery %s", k)
	}
	if got := f.available(s); got != "8.0000" {
		t.Fatalf("available %s", got)
	}
}
