package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
)

// Bonus credit on top-ups and customers' refund requests (ADR-0013), end to
// end through the dashboard routes and the signed webhook.

type topupDetail struct {
	ID            string `json:"id"`
	BonusUSD      string `json:"bonus_usd"`
	Status        string `json:"status"`
	RefundedUSD   string `json:"refunded_usd"`
	RefundableUSD string `json:"refundable_usd"`
	RefundRequest *struct {
		ID         string `json:"id"`
		AmountUSD  string `json:"amount_usd"`
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	} `json:"refund_request"`
}

func (f *topupFixture) detail(s session, id string) topupDetail {
	f.t.Helper()
	rec := f.do(req{method: http.MethodGet, path: "/topups/" + id, cookie: s.cookie})
	var d topupDetail
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
		f.t.Fatalf("GET /topups/%s: %d %s", id, rec.Code, rec.Body)
	}
	return d
}

func (f *topupFixture) askRefund(s session, id string, body map[string]string) *httptest.ResponseRecorder {
	return f.do(req{method: http.MethodPost, path: "/topups/" + id + "/refund-request", body: body, cookie: s.cookie, csrf: s.csrf})
}

func TestBonusFollowsTheTiers(t *testing.T) {
	for cents, want := range map[int]int{1000: 0, 4999: 0, 5000: 150, 7550: 226, 9999: 299, 10000: 500} {
		if got := payments.BonusFor(cents); got != want {
			t.Errorf("BonusFor(%d) = %d, want %d", cents, got, want)
		}
	}
}

func TestBonusIsCreditedWithThePaymentOnce(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	c := f.mustTopup(s, "100.00")
	var last stripetest.Request
	for _, r := range f.stripe.Requests() {
		if r.Path == "/v1/checkout/sessions" {
			last = r
		}
	}
	if got := last.Form.Get("line_items[0][price_data][unit_amount]"); got != "10000" {
		t.Errorf("charged %s, want the amount entered", got)
	}
	if got := last.Form.Get("line_items[0][price_data][product_data][name]"); got != "Unify Compute credit, includes USD 5.00 bonus" {
		t.Errorf("line item %q", got)
	}
	cs := f.stripe.Pay(f.sessionOf(c.ID))
	ev := f.event("checkout.session.completed", stripetest.SessionObject(cs))
	for range 2 {
		f.signed(ev)
	}
	f.pay.Wait()
	if got := f.available(s); got != "105.0000" {
		t.Fatalf("available %s", got)
	}
	if n := f.count(`SELECT count(*) FROM balance_entries WHERE kind = 'topup_bonus'`); n != 1 {
		t.Fatalf("%d bonus entries after a redelivery", n)
	}
	if d := f.detail(s, c.ID); d.BonusUSD != "5.00" || d.RefundableUSD != "100.00" {
		t.Fatalf("top-up %+v", d)
	}
	f.balanced()
}

func TestRefundsAndDisputesTakeTheBonusShare(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	id, pi := f.paidTopup(s, "50.00") // 51.50

	// 20 of 50 refunded: floor(150 × 20 / 50) = 60 cents of bonus go too.
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 5000, 2000)); out != payments.OutcomeRefunded {
		t.Fatalf("refund: %s", out)
	}
	if got := f.available(s); got != "30.9000" {
		t.Fatalf("available %s", got)
	}
	if m := f.mail.to("ada@example.com"); !strings.Contains(m[len(m)-1].text, "Bonus credit removed: USD 0.60") {
		t.Fatalf("refund email: %s", m[len(m)-1].text)
	}
	// A dispute on 10 more: the running share is floor(150 × 30 / 50) = 90.
	if out := f.send("charge.dispute.created", stripetest.DisputeObject("dp_1", pi, 1000, "needs_response")); out != payments.OutcomeDisputed {
		t.Fatalf("dispute: %s", out)
	}
	if got := f.available(s); got != "20.6000" {
		t.Fatalf("available after dispute %s", got)
	}
	// Won: the 10 and its 30 cents of bonus come back.
	if out := f.send("charge.dispute.closed", stripetest.DisputeObject("dp_1", pi, 1000, "won")); out != payments.OutcomeDisputeWon {
		t.Fatalf("won: %s", out)
	}
	if got := f.available(s); got != "30.9000" {
		t.Fatalf("available after won %s", got)
	}
	// The rest: everything that remains of the bonus goes.
	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 5000, 5000)); out != payments.OutcomeRefunded {
		t.Fatalf("rest: %s", out)
	}
	if got := f.available(s); got != "0.0000" {
		t.Fatalf("available %s", got)
	}
	var reversed int
	if err := f.db.QueryRow(context.Background(), `SELECT bonus_reversed_cents FROM topups WHERE id = $1`, id).Scan(&reversed); err != nil || reversed != 150 {
		t.Fatalf("bonus reversed %d %v", reversed, err)
	}
	f.balanced()
}

func TestOperatorRefundMustFitWithItsBonusShare(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	id, _ := f.paidTopup(s, "100.00") // 105
	if _, err := f.pay.RequestRefund(context.Background(), payments.RefundRequest{TopupID: id, Actor: "ops", Reason: "x"}); err != nil {
		t.Fatalf("a full refund that fits: %v", err)
	}
	g := newRefundFixture(t)
	s = g.activeUser("bob@example.com")
	id, _ = g.paidTopup(s, "100.00")
	if _, err := g.db.Exec(context.Background(), `UPDATE accounts SET available = available - $1 WHERE id = $2`, int64(6000)*5000, g.accountOf("bob@example.com")); err != nil {
		t.Fatal(err)
	}
	// 45.00 left: 42.86 and its floor(500 × 4286 / 10000) = 2.14 of bonus fit exactly.
	_, err := g.pay.RequestRefund(context.Background(), payments.RefundRequest{TopupID: id, Actor: "ops", Reason: "x"})
	if err == nil || !strings.Contains(err.Error(), "Refund at most USD 42.86") {
		t.Fatalf("want a refusal naming 42.86, got %v", err)
	}
}

func TestRefundRequestIsClosedByTheRefund(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	id, pi := f.paidTopup(s, "50.00")

	wantErr(t, f.askRefund(s, id, map[string]string{"amount_usd": "50.01"}), http.StatusBadRequest, "invalid_request", "invalid_amount")
	wantErr(t, f.askRefund(s, id, map[string]string{"reason": strings.Repeat("x", 501)}), http.StatusBadRequest, "invalid_request", "invalid_reason")
	rec := f.askRefund(s, id, map[string]string{"amount_usd": "20", "reason": "Finished early"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.askRefund(s, id, nil), http.StatusConflict, "conflict", "refund_request_open")
	f.pay.Wait()
	ops := f.mail.to(opsAddr)
	if len(ops) != 1 || !strings.Contains(ops[0].text, "Amount asked: USD 20.00") || !strings.Contains(ops[0].text, "Finished early") {
		t.Fatalf("operator notice: %+v", ops)
	}
	if got := f.available(s); got != "51.5000" {
		t.Fatalf("a request moved money: %s", got)
	}
	eve := f.activeUser("eve@example.com")
	wantErr(t, f.askRefund(eve, id, nil), http.StatusNotFound, "not_found", "not_found")
	member := f.join(s, "mo@example.com", "member")
	wantErr(t, f.askRefund(member, id, nil), http.StatusForbidden, "permission_error", "role_required")

	if out := f.send("charge.refunded", stripetest.ChargeObject(pi, 5000, 2000)); out != payments.OutcomeRefunded {
		t.Fatalf("refund: %s", out)
	}
	d := f.detail(s, id)
	if d.RefundRequest == nil || d.RefundRequest.Status != "refunded" || d.RefundableUSD != "30.00" || d.Status != "partially_refunded" {
		t.Fatalf("after refund: %+v %+v", d, d.RefundRequest)
	}
	f.balanced()
}

func TestRefundRequestCancelAndDecline(t *testing.T) {
	f := newRefundFixture(t)
	s := f.activeUser("ada@example.com")
	id, _ := f.paidTopup(s, "20.00")

	var rr struct {
		ID        string `json:"id"`
		AmountUSD string `json:"amount_usd"`
	}
	rec := f.askRefund(s, id, nil)
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &rr) != nil || rr.AmountUSD != "20.00" {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	eve := f.activeUser("eve@example.com")
	wantErr(t, f.do(req{method: http.MethodDelete, path: "/refund-requests/" + rr.ID, cookie: eve.cookie, csrf: eve.csrf}), http.StatusNotFound, "not_found", "not_found")
	if rec := f.do(req{method: http.MethodDelete, path: "/refund-requests/" + rr.ID, cookie: s.cookie, csrf: s.csrf}); rec.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(req{method: http.MethodDelete, path: "/refund-requests/" + rr.ID, cookie: s.cookie, csrf: s.csrf}), http.StatusConflict, "conflict", "refund_request_closed")

	f.clock.advance(time.Minute)
	rec = f.askRefund(s, id, map[string]string{"amount_usd": "5"})
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &rr) != nil {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body)
	}
	list, err := f.pay.OpenRefundRequests(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != rr.ID {
		t.Fatalf("open requests %+v %v", list, err)
	}
	sent, err := f.pay.DeclineRefundRequest(context.Background(), rr.ID, "The credit was used by jobs.", "spent", "ops")
	if err != nil || !sent {
		t.Fatalf("decline: %v %v", sent, err)
	}
	if _, err := f.pay.DeclineRefundRequest(context.Background(), rr.ID, "again", "again", "ops"); err == nil {
		t.Fatal("declined twice")
	}
	if d := f.detail(s, id); d.RefundRequest.Status != "declined" || d.RefundRequest.Resolution != "The credit was used by jobs." {
		t.Fatalf("after decline: %+v", d.RefundRequest)
	}
	if m := f.mail.to("ada@example.com"); m[len(m)-1].kind != "refund-declined" {
		t.Fatalf("customer email: %s", kinds(m))
	}
	acts := strings.Join(f.auditActions(f.accountOf("ada@example.com")), ",")
	for _, a := range []string{"refund_requested", "refund_request_cancelled", "refund_request_declined"} {
		if !strings.Contains(acts, a) {
			t.Errorf("audit lacks %s: %s", a, acts)
		}
	}
	if got := f.available(s); got != "20.0000" {
		t.Fatalf("available %s", got)
	}
}
