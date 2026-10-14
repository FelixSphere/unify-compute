package dashboard

import (
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
)

// newMailedTopupFixture is newTopupFixture with the top-up receipt email
// wired as main wires it: the dashboard's mailer and a notifier on the same
// database.
func newMailedTopupFixture(t *testing.T) *topupFixture {
	t.Helper()
	f := newFixture(t)
	fake := stripetest.New(t)
	fake.Now = f.clock.now
	bill := billing.New(f.db, f.clock.now)
	pay := payments.New(payments.Config{DB: f.db, Accounts: f.acc, Billing: bill, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Settings: payments.Settings{SecretKey: stripetest.Key, WebhookSecret: stripetest.Secret, Environment: "staging"},
		Stripe:   fake.Client(), Mailer: f.h.mail, Notifier: notify.New(f.db, f.h.log, f.clock.now)})
	h, err := New(Config{Accounts: f.acc, Mailer: f.h.mail, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Ledger: bill, Payments: pay})
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	return &topupFixture{fixture: f, stripe: fake, pay: pay, bill: bill}
}

func receipts(f *topupFixture, addr string) []sent {
	var out []sent
	for _, m := range f.mail.to(addr) {
		if m.kind == mailer.KindTopupReceived {
			m.text = strings.ReplaceAll(m.text, "\r\n", "\n")
			out = append(out, m)
		}
	}
	return out
}

// The credit is followed by one "Top-up received" with the amount, the new
// balance and Stripe's receipt link. Stripe redelivering the event, or
// sending async_payment_succeeded for the same session, sends nothing more.
func TestCreditedTopupIsEmailedOnceWithItsReceipt(t *testing.T) {
	f := newMailedTopupFixture(t)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "20.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))
	body := f.event("checkout.session.completed", stripetest.SessionObject(cs))
	if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeCredited {
		t.Fatalf("outcome %s", out)
	}
	f.pay.Wait()
	got := receipts(f, "ada@example.com")
	if len(got) != 1 {
		t.Fatalf("%d receipts: %+v", len(got), f.mail.all())
	}
	for _, want := range []string{
		"USD 20.00 was added to your Unify Compute balance.",
		"View receipt:\nhttps://pay.stripe.com/receipts/payment/" + cs.PaymentIntent + "\n",
		"Amount: USD 20.00\n", "Available balance: USD 20.0000\n", "Top-up: " + c.ID + "\n",
	} {
		if !strings.Contains(got[0].text, want) {
			t.Errorf("receipt lacks %q:\n%s", want, got[0].text)
		}
	}

	if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeDuplicate {
		t.Fatalf("redelivery %s", out)
	}
	if out := outcomeOf(t, f.signed(f.event("checkout.session.async_payment_succeeded", stripetest.SessionObject(cs)))); out != payments.OutcomeAlreadyCredited {
		t.Fatalf("second event %s", out)
	}
	f.pay.Wait()
	if n := len(receipts(f, "ada@example.com")); n != 1 {
		t.Fatalf("%d receipts after redelivery", n)
	}
}

// Stripe not answering the receipt lookup leaves the credit standing and the
// email going out without the link: its button opens Billing instead.
func TestTopupEmailWithoutAReceiptLinksBilling(t *testing.T) {
	f := newMailedTopupFixture(t)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "20.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))
	f.stripe.FailNext(10)
	if out := outcomeOf(t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(cs)))); out != payments.OutcomeCredited {
		t.Fatalf("outcome %s", out)
	}
	f.pay.Wait()
	got := receipts(f, "ada@example.com")
	if len(got) != 1 || !strings.Contains(got[0].text, "View billing:\n"+testOrigin+"/billing\n") || strings.Contains(got[0].text, "pay.stripe.com") {
		t.Fatalf("receipts %+v", got)
	}
}

// SES refusing the receipt does not touch the credit or the webhook's
// answer.
func TestRefusedTopupEmailDoesNotFailTheWebhook(t *testing.T) {
	f := newMailedTopupFixture(t)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "20.00")
	f.mail.fail.Store(true)
	if out := f.pay_(c.ID); out != payments.OutcomeCredited {
		t.Fatalf("outcome %s", out)
	}
	f.pay.Wait()
	if got := f.available(s); got != "20.0000" {
		t.Fatalf("available %s", got)
	}
	var state string
	if err := f.db.QueryRow(t.Context(), `SELECT state FROM notifications WHERE subject_kind = 'topup' AND subject_id = $1`, c.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("notification %q %v", state, err)
	}
}
