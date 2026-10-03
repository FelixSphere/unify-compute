package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
)

// Top-ups end to end against the Stripe fake: the dashboard routes, the
// Checkout Session compute asks Stripe for, and the signed webhook that is
// the only thing allowed to credit (ADR-0011 §4, spec/accounts.md).

type topupFixture struct {
	*fixture
	stripe *stripetest.Server
	pay    *payments.Service
	bill   *billing.Local
	events int
}

func newTopupFixture(t *testing.T, withStripe bool) *topupFixture {
	t.Helper()
	f := newFixture(t)
	fake := stripetest.New(t)
	fake.Now = f.clock.now
	bill := billing.New(f.db, f.clock.now)
	cfg := payments.Config{DB: f.db, Accounts: f.acc, Billing: bill, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Settings: payments.Settings{SecretKey: stripetest.Key, WebhookSecret: stripetest.Secret, Environment: "staging"}}
	if withStripe {
		cfg.Stripe = fake.Client()
	}
	pay := payments.New(cfg)
	h, err := New(Config{Accounts: f.acc, Mailer: f.h.mail, AppOrigin: testOrigin, Log: f.h.log, Now: f.clock.now,
		Ledger: bill, Payments: pay})
	if err != nil {
		t.Fatal(err)
	}
	f.h = h
	return &topupFixture{fixture: f, stripe: fake, pay: pay, bill: bill}
}

type createdTopup struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	CheckoutURL string `json:"checkout_url"`
}

func (f *topupFixture) topup(s session, amount string) *httptest.ResponseRecorder {
	return f.do(req{method: http.MethodPost, path: "/topups", body: map[string]string{"amount_usd": amount}, cookie: s.cookie, csrf: s.csrf})
}

func (f *topupFixture) mustTopup(s session, amount string) createdTopup {
	f.t.Helper()
	rec := f.topup(s, amount)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("POST /topups %s: %d %s", amount, rec.Code, rec.Body)
	}
	var c createdTopup
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// sessionOf is the Checkout Session compute created for a top-up.
func (f *topupFixture) sessionOf(topupID string) string {
	f.t.Helper()
	var sid string
	if err := f.db.QueryRow(context.Background(), `SELECT stripe_checkout_session_id FROM topups WHERE id = $1`, topupID).Scan(&sid); err != nil {
		f.t.Fatalf("session of %s: %v", topupID, err)
	}
	return sid
}

// deliver posts a webhook body as Stripe does: no Origin, no cookie, the
// signature header, on the app host.
func (f *topupFixture) deliver(body []byte, signature string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, Prefix+"/stripe/webhook", bytes.NewReader(body))
	r.Host = testHost
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.Header.Set("User-Agent", "Stripe/1.0 (+https://stripe.com/docs/webhooks)")
	if signature != "" {
		r.Header.Set("Stripe-Signature", signature)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func (f *topupFixture) signed(body []byte) *httptest.ResponseRecorder {
	return f.deliver(body, payments.Sign(body, stripetest.Secret, f.clock.now()))
}

func (f *topupFixture) event(typ string, obj any) []byte {
	f.events++
	return stripetest.Event(fmt.Sprintf("evt_test_%d", f.events), typ, false, obj)
}

func outcomeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook %d: %s", rec.Code, rec.Body)
	}
	var b struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Outcome
}

// pay completes the top-up's session at the fake and delivers the signed
// checkout.session.completed event.
func (f *topupFixture) pay_(id string) string {
	f.t.Helper()
	cs := f.stripe.Pay(f.sessionOf(id))
	return outcomeOf(f.t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(cs))))
}

type topupList struct {
	Data []struct {
		ID          string  `json:"id"`
		AmountUSD   string  `json:"amount_usd"`
		Status      string  `json:"status"`
		PaidAt      *int64  `json:"paid_at"`
		ReceiptURL  *string `json:"receipt_url"`
		RefundedUSD string  `json:"refunded_usd"`
	} `json:"data"`
	Enabled bool `json:"topups_enabled"`
}

func (f *topupFixture) list(s session) topupList {
	f.t.Helper()
	rec := f.do(req{method: http.MethodGet, path: "/topups", cookie: s.cookie})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET /topups: %d %s", rec.Code, rec.Body)
	}
	var l topupList
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		f.t.Fatal(err)
	}
	return l
}

func (f *topupFixture) available(s session) string {
	f.t.Helper()
	rec := f.do(req{method: http.MethodGet, path: "/balance", cookie: s.cookie})
	var b struct {
		AvailableUSD string `json:"available_usd"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &b) != nil {
		f.t.Fatalf("GET /balance: %d %s", rec.Code, rec.Body)
	}
	return b.AvailableUSD
}

func (f *topupFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *topupFixture) balanced() {
	f.t.Helper()
	m, err := f.bill.Audit(context.Background(), nil)
	if err != nil || len(m) != 0 {
		f.t.Fatalf("balance invariant: %+v %v", m, err)
	}
}

func TestTopupFullFlowCreditsOnlyThroughTheWebhook(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	acct := f.accountOf("ada@example.com")

	c := f.mustTopup(s, "20.00")
	if !strings.HasPrefix(c.ID, "tu_") || c.Status != "open" || !strings.HasPrefix(c.CheckoutURL, "https://checkout.stripe.com/") {
		t.Fatalf("created %+v", c)
	}

	// What compute asked Stripe for.
	if got := f.stripe.Calls(); !slices.Equal(got, []string{"POST /v1/customers", "POST /v1/checkout/sessions"}) {
		t.Fatalf("stripe calls %v", got)
	}
	reqs := f.stripe.Requests()
	cust, sess := reqs[0], reqs[1]
	acctID := strconv.FormatInt(acct, 10)
	for _, r := range reqs {
		if r.Version != payments.APIVersion || r.IdempotencyKey == "" {
			t.Errorf("%s %s: version %q, idempotency key %q", r.Method, r.Path, r.Version, r.IdempotencyKey)
		}
	}
	if cust.Form.Get("email") != "ada@example.com" || cust.Form.Get("metadata[product]") != "unify-compute" ||
		cust.Form.Get("metadata[account_id]") != acctID || cust.Form.Get("metadata[environment]") != "staging" {
		t.Errorf("customer form %v", cust.Form)
	}
	want := map[string]string{
		"mode":                                              "payment",
		"line_items[0][price_data][currency]":               "usd",
		"line_items[0][price_data][unit_amount]":            "2000",
		"line_items[0][quantity]":                           "1",
		"payment_method_types[0]":                           "card",
		"client_reference_id":                               c.ID,
		"success_url":                                       testOrigin + "/billing?topup=" + c.ID + "&checkout=success",
		"cancel_url":                                        testOrigin + "/billing?topup=" + c.ID + "&checkout=cancelled",
		"metadata[product]":                                 "unify-compute",
		"metadata[account_id]":                              acctID,
		"metadata[topup_id]":                                c.ID,
		"metadata[environment]":                             "staging",
		"payment_intent_data[metadata][product]":            "unify-compute",
		"payment_intent_data[metadata][topup_id]":           c.ID,
		"saved_payment_method_options[payment_method_save]": "disabled",
		"expires_at":                                        strconv.FormatInt(f.clock.now().Add(payments.CheckoutTTL).Unix(), 10),
	}
	for k, v := range want {
		if got := sess.Form.Get(k); got != v {
			t.Errorf("session %s = %q, want %q", k, got, v)
		}
	}
	if sess.Form.Get("customer") == "" || sess.Form.Get("payment_intent_data[setup_future_usage]") != "" || sess.Form.Get("line_items[1][quantity]") != "" {
		t.Errorf("session form %v", sess.Form)
	}
	if sess.IdempotencyKey != "unify-compute:topup:"+c.ID || cust.IdempotencyKey != "unify-compute:staging:customer:"+acctID {
		t.Errorf("idempotency keys: session %q, customer %q", sess.IdempotencyKey, cust.IdempotencyKey)
	}
	if sess.Form.Get("line_items[0][price_data][product_data][metadata][product]") != "unify-compute" {
		t.Errorf("the inline product is untagged: %v", sess.Form)
	}

	// Open, listed, and nothing credited: coming back from Checkout is not
	// a payment.
	l := f.list(s)
	if !l.Enabled || len(l.Data) != 1 || l.Data[0].ID != c.ID || l.Data[0].Status != "open" || l.Data[0].AmountUSD != "20.00" || l.Data[0].PaidAt != nil {
		t.Fatalf("list before payment %+v", l)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("available before the webhook %s", got)
	}

	if out := f.pay_(c.ID); out != payments.OutcomeCredited {
		t.Fatalf("outcome %s", out)
	}
	f.pay.Wait()
	if got := f.available(s); got != "20.0000" {
		t.Fatalf("available after the webhook %s", got)
	}
	rec := f.do(req{method: http.MethodGet, path: "/topups/" + c.ID, cookie: s.cookie})
	var one struct {
		Status     string  `json:"status"`
		PaidAt     *int64  `json:"paid_at"`
		ReceiptURL *string `json:"receipt_url"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &one) != nil {
		t.Fatalf("GET /topups/{id}: %d %s", rec.Code, rec.Body)
	}
	if one.Status != "credited" || one.PaidAt == nil || one.ReceiptURL == nil || !strings.HasPrefix(*one.ReceiptURL, "https://pay.stripe.com/receipts/") {
		t.Fatalf("top-up after payment %s", rec.Body)
	}

	// The history names the top-up; 20.00 USD is exactly 10,000,000 units.
	rec = f.do(req{method: http.MethodGet, path: "/transactions", cookie: s.cookie})
	var hist struct {
		Data []entryView `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &hist) != nil || len(hist.Data) != 1 {
		t.Fatalf("history %d %s", rec.Code, rec.Body)
	}
	e := hist.Data[0]
	if e.Kind != "topup" || e.AmountUSD != "20.0000" || e.Ref == nil || e.Ref.Type != "topup" || e.Ref.ID != c.ID || e.JobID != nil {
		t.Fatalf("history entry %+v", e)
	}
	entries, _ := f.bill.Entries(context.Background(), acct)
	if len(entries) != 1 || entries[0].AvailableDelta != 10_000_000 || entries[0].IdempotencyKey != "topup:"+c.ID {
		t.Fatalf("entries %+v", entries)
	}
	if acts := f.auditActions(acct); !slices.Contains(acts, "topup_created") || !slices.Contains(acts, "topup_credited") {
		t.Fatalf("audit %v", acts)
	}
	if n := f.count(`SELECT count(*) FROM audit_events WHERE action = 'topup_credited' AND actor_type = 'stripe'`); n != 1 {
		t.Fatalf("topup_credited by stripe: %d", n)
	}
	f.balanced()

	// Another account sees none of it.
	other := f.activeUser("bob@example.com")
	if l := f.list(other); len(l.Data) != 0 {
		t.Fatalf("bob sees %+v", l)
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/topups/" + c.ID, cookie: other.cookie}), http.StatusNotFound, "not_found", "not_found")
}

func TestTopupWebhookCreditsOnceWhateverIsRedelivered(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "25.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))
	obj := stripetest.SessionObject(cs)
	completed := f.event("checkout.session.completed", obj)

	if out := outcomeOf(t, f.signed(completed)); out != payments.OutcomeCredited {
		t.Fatalf("first delivery %s", out)
	}
	// Stripe redelivers the same event (a retry, or `stripe events resend`).
	if out := outcomeOf(t, f.signed(completed)); out != payments.OutcomeDuplicate {
		t.Fatalf("redelivery %s", out)
	}
	// A second event about the same session.
	if out := outcomeOf(t, f.signed(f.event("checkout.session.async_payment_succeeded", obj))); out != payments.OutcomeAlreadyCredited {
		t.Fatalf("second event %s", out)
	}
	// An expiry arriving late never undoes a credit.
	if out := outcomeOf(t, f.signed(f.event("checkout.session.expired", obj))); out != payments.OutcomeNoChange {
		t.Fatalf("late expiry %s", out)
	}

	// Many distinct events for the same paid session at once.
	var wg sync.WaitGroup
	outs := make([]string, 8)
	bodies := make([][]byte, len(outs))
	for i := range bodies {
		bodies[i] = f.event("checkout.session.completed", obj)
	}
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := f.signed(bodies[i])
			if rec.Code == http.StatusOK {
				var b struct{ Outcome string }
				_ = json.Unmarshal(rec.Body.Bytes(), &b)
				outs[i] = b.Outcome
			} else {
				outs[i] = rec.Body.String()
			}
		}(i)
	}
	wg.Wait()
	for _, o := range outs {
		if o != payments.OutcomeAlreadyCredited {
			t.Fatalf("concurrent outcomes %v", outs)
		}
	}
	f.pay.Wait()
	if got := f.available(s); got != "25.0000" {
		t.Fatalf("available %s after every redelivery", got)
	}
	if n := f.count(`SELECT count(*) FROM balance_entries WHERE kind = 'topup'`); n != 1 {
		t.Fatalf("%d topup entries", n)
	}
	f.balanced()
}

func TestTopupWebhookSignatures(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "10.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))
	body := f.event("checkout.session.completed", stripetest.SessionObject(cs))
	now := f.clock.now()

	tampered := bytes.Replace(body, []byte(`"amount_total":1000`), []byte(`"amount_total":100000`), 1)
	if bytes.Equal(tampered, body) {
		t.Fatal("tamper did not change the body")
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no signature":         f.deliver(body, ""),
		"wrong secret":         f.deliver(body, payments.Sign(body, "whsec_unify_api_endpoint", now)),
		"stale timestamp":      f.deliver(body, payments.Sign(body, stripetest.Secret, now.Add(-6*time.Minute))),
		"future timestamp":     f.deliver(body, payments.Sign(body, stripetest.Secret, now.Add(6*time.Minute))),
		"tampered body":        f.deliver(tampered, payments.Sign(body, stripetest.Secret, now)),
		"garbage signature":    f.deliver(body, "t=abc,v1=zz"),
		"signature of nothing": f.deliver(body, payments.Sign(nil, stripetest.Secret, now)),
	} {
		if rec.Code != http.StatusBadRequest || decodeErr(t, rec).Error.Code != "invalid_signature" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if n := f.count(`SELECT count(*) FROM stripe_events`); n != 0 {
		t.Fatalf("a refused delivery stored %d events", n)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("a refused delivery credited: %s", got)
	}

	// The webhook needs no session and no Origin, but only on the app host.
	r := httptest.NewRequest(http.MethodPost, Prefix+"/stripe/webhook", bytes.NewReader(body))
	r.Host = "api.compute.test"
	r.Header.Set("Stripe-Signature", payments.Sign(body, stripetest.Secret, now))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("webhook on the API host: %d", rec.Code)
	}
	if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeCredited {
		t.Fatalf("the genuine delivery: %s", out)
	}
}

func TestTopupWebhookIgnoresEverythingNotCompute(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "10.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))

	// Unify API's own Checkout payments on the shared account: no product
	// tag, or another product's, even if a field happens to collide.
	unifyAPI := stripetest.SessionObject(payments.CheckoutSession{ID: "cs_live_unifyapi", Status: "complete", PaymentStatus: "paid",
		Mode: "payment", Currency: "usd", AmountTotal: 500000, ClientReferenceID: c.ID, PaymentIntent: "pi_unifyapi",
		Metadata: map[string]string{"user_id": "42"}})
	tagged := stripetest.SessionObject(payments.CheckoutSession{ID: "cs_unifyapi2", Status: "complete", PaymentStatus: "paid",
		Mode: "payment", Currency: "usd", AmountTotal: 1000, ClientReferenceID: c.ID, PaymentIntent: "pi_unifyapi2",
		Metadata: map[string]string{"product": "unifyapi", "topup_id": c.ID}})
	otherEnv := stripetest.SessionObject(cs)
	otherEnv["metadata"] = map[string]string{"product": "unify-compute", "environment": "production", "topup_id": c.ID,
		"account_id": cs.Metadata["account_id"]}
	for name, body := range map[string][]byte{
		"unify api checkout":               f.event("checkout.session.completed", unifyAPI),
		"another product's tag":            f.event("checkout.session.completed", tagged),
		"compute, another environment":     f.event("checkout.session.completed", otherEnv),
		"compute, live mode on a test key": stripetest.Event("evt_live_1", "checkout.session.completed", true, stripetest.SessionObject(cs)),
		"unify api refund":                 f.event("charge.refunded", map[string]any{"id": "ch_unifyapi", "object": "charge", "payment_intent": "pi_unifyapi", "amount_refunded": 500}),
		"unify api dispute":                f.event("charge.dispute.created", map[string]any{"id": "dp_1", "object": "dispute", "charge": "ch_x", "payment_intent": "pi_unifyapi", "amount": 500}),
		"unify api payment intent":         f.event("payment_intent.succeeded", map[string]any{"id": "pi_unifyapi", "object": "payment_intent"}),
		"unify api invoice":                f.event("invoice.paid", map[string]any{"id": "in_1", "object": "invoice"}),
		"customer created by the console":  f.event("customer.created", map[string]any{"id": "cus_console", "object": "customer"}),
	} {
		if out := outcomeOf(t, f.signed(body)); out != payments.OutcomeIgnored {
			t.Errorf("%s: outcome %s", name, out)
		}
	}
	if n := f.count(`SELECT count(*) FROM stripe_events`); n != 0 {
		t.Fatalf("stored %d events that are not compute's", n)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("another product's event credited compute: %s", got)
	}
	if l := f.list(s); l.Data[0].Status != "open" {
		t.Fatalf("top-up %s", l.Data[0].Status)
	}

	// Compute's own payment still credits; a refund or dispute on it is
	// recorded for B2 and changes nothing yet.
	if out := outcomeOf(t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(cs)))); out != payments.OutcomeCredited {
		t.Fatalf("compute's own: %s", out)
	}
	refund := f.event("charge.refunded", map[string]any{"id": "ch_c", "object": "charge", "payment_intent": cs.PaymentIntent, "amount_refunded": 1000})
	if out := outcomeOf(t, f.signed(refund)); out != payments.OutcomeRecorded {
		t.Fatalf("refund on a top-up: %s", out)
	}
	if n := f.count(`SELECT count(*) FROM stripe_events WHERE type = 'charge.refunded' AND topup_id = $1 AND outcome = 'recorded'`, c.ID); n != 1 {
		t.Fatalf("refund recorded %d times", n)
	}
	f.pay.Wait()
	if got := f.available(s); got != "10.0000" {
		t.Fatalf("available %s", got)
	}
	f.balanced()
}

func TestTopupWebhookRejectsASessionThatDoesNotMatch(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "50.00")
	cs := f.stripe.Pay(f.sessionOf(c.ID))

	cheap := cs
	cheap.AmountTotal = 100
	eur := cs
	eur.Currency = "eur"
	elsewhere := cs
	elsewhere.Metadata = map[string]string{"product": "unify-compute", "environment": "staging", "topup_id": c.ID, "account_id": "999999"}
	forged := cs
	forged.ID = "cs_test_not_ours"
	for name, obj := range map[string]payments.CheckoutSession{"amount": cheap, "currency": eur, "account": elsewhere, "session": forged} {
		if out := outcomeOf(t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(obj)))); out != payments.OutcomeRejected {
			t.Errorf("%s mismatch: outcome %s", name, out)
		}
	}
	unknown := cs
	unknown.ClientReferenceID = "tu_01J8Q6ZK3R7Y9W2X4V6T8S0A1B"
	unknown.Metadata = map[string]string{"product": "unify-compute", "environment": "staging", "topup_id": unknown.ClientReferenceID}
	if out := outcomeOf(t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(unknown)))); out != payments.OutcomeRejected {
		t.Errorf("unknown top-up: outcome %s", out)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("a mismatched session credited %s", got)
	}
	if n := f.count(`SELECT count(*) FROM stripe_events WHERE outcome = 'rejected'`); n != 5 {
		t.Fatalf("%d rejected events stored", n)
	}
}

func TestTopupLimits(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")

	for _, bad := range []string{"", "abc", "-5", "10.001", "1e3", "ten"} {
		e := wantErr(t, f.topup(s, bad), http.StatusBadRequest, "invalid_request", "invalid_amount")
		if e.Error.Param != "amount_usd" {
			t.Errorf("%q: param %q", bad, e.Error.Param)
		}
	}
	for _, out := range []string{"9.99", "100.01", "0.50", "1000"} {
		e := wantErr(t, f.topup(s, out), http.StatusForbidden, "permission_error", "topup_limit_exceeded")
		if e.Error.Param != "amount_usd" || !strings.Contains(e.Error.Message, "USD") {
			t.Errorf("%s: %+v", out, e.Error)
		}
	}
	if calls := f.stripe.Calls(); len(calls) != 0 {
		t.Fatalf("refused amounts reached Stripe: %v", calls)
	}

	// The balance cap counts available and held: 100 + 95 leaves room for 5,
	// under the minimum, so 10 is refused and says the balance is full.
	if out := f.pay_(f.mustTopup(s, "100.00").ID); out != payments.OutcomeCredited {
		t.Fatal(out)
	}
	if out := f.pay_(f.mustTopup(s, "95").ID); out != payments.OutcomeCredited {
		t.Fatal(out)
	}
	e := wantErr(t, f.topup(s, "10.00"), http.StatusForbidden, "permission_error", "topup_limit_exceeded")
	if !strings.Contains(e.Error.Message, "200.00") {
		t.Errorf("balance cap message %q", e.Error.Message)
	}

	// Three top-ups a day, counted over a rolling 24 hours.
	b := f.activeUser("bob@example.com")
	for i := 0; i < 3; i++ {
		if out := f.pay_(f.mustTopup(b, "10").ID); out != payments.OutcomeCredited {
			t.Fatal(out)
		}
	}
	e = wantErr(t, f.topup(b, "10"), http.StatusForbidden, "permission_error", "topup_limit_exceeded")
	if e.Error.Param != "" || !strings.Contains(e.Error.Message, "24 hours") {
		t.Errorf("daily limit %+v", e.Error)
	}
	f.clock.advance(24*time.Hour + time.Second)
	b = sessionFrom(t, f.login("bob@example.com", goodPass)) // the old session idled out
	f.mustTopup(b, "10")

	// The operator raises one account's limits; the others keep the default.
	c := f.activeUser("cy@example.com")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET topup_max_cents = 15000, balance_max_cents = 50000 WHERE id = $1`,
		f.accountOf("cy@example.com")); err != nil {
		t.Fatal(err)
	}
	f.mustTopup(c, "150")
	wantErr(t, f.topup(b, "150"), http.StatusForbidden, "permission_error", "topup_limit_exceeded")

	// Abandoned top-ups do not count against the day.
	d := f.activeUser("dee@example.com")
	for i := 0; i < 5; i++ {
		f.mustTopup(d, "10")
	}
	if n := f.count(`SELECT count(*) FROM topups WHERE account_id = $1 AND status = 'expired'`, f.accountOf("dee@example.com")); n != 4 {
		t.Fatalf("%d expired, want 4 replaced", n)
	}

	// A suspended account cannot top up.
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET status = 'suspended', suspended_reason = 'operator' WHERE id = $1`,
		f.accountOf("dee@example.com")); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.topup(d, "10"), http.StatusForbidden, "authentication_error", "account_suspended")
	f.balanced()
}

func TestTopupReplacesAnOpenOneAndNeverAPaidOne(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	a := f.mustTopup(s, "10")
	aSession := f.sessionOf(a.ID)

	// Backing out of Checkout and trying again: the first session is
	// expired at Stripe, so the old tab can no longer be paid.
	f.clock.advance(time.Second) // the list is newest first
	b := f.mustTopup(s, "30")
	if got := f.stripe.Session(aSession).Status; got != "expired" {
		t.Fatalf("first session %s at Stripe", got)
	}
	if !slices.Contains(f.stripe.Calls(), "POST /v1/checkout/sessions/"+aSession+"/expire") {
		t.Fatalf("calls %v", f.stripe.Calls())
	}
	if n := f.count(`SELECT count(*) FROM stripe_events`); n != 0 {
		t.Fatal("expiring touched stripe_events")
	}
	l := f.list(s)
	if len(l.Data) != 2 || l.Data[0].ID != b.ID || l.Data[0].Status != "open" || l.Data[1].Status != "expired" {
		t.Fatalf("list %+v", l)
	}
	// Stripe's own expiry event for it arrives later and changes nothing.
	expired := stripetest.SessionObject(f.stripe.Session(aSession))
	if out := outcomeOf(t, f.signed(f.event("checkout.session.expired", expired))); out != payments.OutcomeNoChange {
		t.Fatalf("expired event %s", out)
	}

	// Paid at Stripe, webhook not here yet: Stripe refuses the expiry, and a
	// new top-up waits for the payment to land.
	f.stripe.Pay(f.sessionOf(b.ID))
	e := wantErr(t, f.topup(s, "10"), http.StatusConflict, "conflict", "topup_open")
	var withID struct {
		Error struct {
			TopupID string `json:"topup_id"`
		}
	}
	_ = json.Unmarshal(f.topup(s, "10").Body.Bytes(), &withID)
	if withID.Error.TopupID != b.ID || e.Error.Message == "" {
		t.Fatalf("topup_open names %q, want %s", withID.Error.TopupID, b.ID)
	}
	if out := outcomeOf(t, f.signed(f.event("checkout.session.completed", stripetest.SessionObject(f.stripe.Session(f.sessionOf(b.ID)))))); out != payments.OutcomeCredited {
		t.Fatal(out)
	}
	c := f.mustTopup(s, "10").ID

	// An open session Stripe expired on its own, before the event: found
	// expired on Stripe and replaced.
	f.stripe.Expire(f.sessionOf(c))
	f.mustTopup(s, "10")
	var st string
	_ = f.db.QueryRow(context.Background(), `SELECT status FROM topups WHERE id = $1`, c).Scan(&st)
	if st != "expired" {
		t.Fatalf("self-expired top-up is %s", st)
	}
	if got := f.available(s); got != "30.0000" {
		t.Fatalf("available %s", got)
	}
}

func TestTopupStripeFailureChargesNothing(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")

	f.stripe.FailNext(1)
	wantErr(t, f.topup(s, "10"), http.StatusBadGateway, "payment_provider_error", "stripe_unavailable")
	c := f.mustTopup(s, "10") // the customer now exists
	if out := f.pay_(c.ID); out != payments.OutcomeCredited {
		t.Fatal(out)
	}
	f.pay.Wait() // the receipt lookup must not take the failure meant for the session
	f.stripe.FailNext(1)
	wantErr(t, f.topup(s, "20"), http.StatusBadGateway, "payment_provider_error", "stripe_unavailable")
	if n := f.count(`SELECT count(*) FROM topups`); n != 1 {
		t.Fatalf("%d top-ups after a failed session create, want only the paid one", n)
	}
	if l := f.list(s); len(l.Data) != 1 || l.Data[0].ID != c.ID {
		t.Fatalf("list %+v", l)
	}
	f.mustTopup(s, "20")
}

func TestTopupsWithoutStripeKeys(t *testing.T) {
	f := newTopupFixture(t, false)
	s := f.activeUser("ada@example.com")
	wantErr(t, f.topup(s, "20"), http.StatusServiceUnavailable, "topups_unavailable", "topups_unavailable")
	if l := f.list(s); l.Enabled || len(l.Data) != 0 {
		t.Fatalf("list %+v", l)
	}
	body := f.event("checkout.session.completed", map[string]any{"id": "cs_x"})
	rec := f.signed(body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("webhook without keys: %d %s", rec.Code, rec.Body)
	}
	if len(f.stripe.Requests()) != 0 {
		t.Fatal("reached Stripe without keys")
	}
}

func TestTopupCreateNeedsSessionAndCSRF(t *testing.T) {
	f := newTopupFixture(t, true)
	s := f.activeUser("ada@example.com")
	body := map[string]string{"amount_usd": "20"}
	wantErr(t, f.do(req{method: http.MethodPost, path: "/topups", body: body}), http.StatusUnauthorized, "authentication_error", "session_required")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/topups", body: body, cookie: s.cookie}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/topups", body: body, cookie: s.cookie, csrf: s.csrf, origin: strp("https://evil.example")}),
		http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/topups"}), http.StatusUnauthorized, "authentication_error", "session_required")
	if len(f.stripe.Requests()) != 0 {
		t.Fatal("an unauthenticated request reached Stripe")
	}
}
