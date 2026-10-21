package mailer

import (
	"context"
	"strings"
	"testing"
)

// The B2 messages, golden like every other (task B2). The refund policy
// sentence is pending legal review; the golden file is where its wording
// is pinned.
var refundMessages = []struct {
	kind string
	send func(m *Mailer, to string) error
}{
	{"refund-issued", func(m *Mailer, to string) error {
		return m.RefundIssued(context.Background(), to, Refund{TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", AmountUSD: "5.00",
			TopupAmountUSD: "20.00", AvailableUSD: "12.3400", At: at})
	}},
	{"account-suspended", func(m *Mailer, to string) error {
		return m.AccountSuspended(context.Background(), to, Suspension{TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", AmountUSD: "20.00", At: at})
	}},
	{"refund-issued-bonus", func(m *Mailer, to string) error {
		return m.RefundIssued(context.Background(), to, Refund{TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", AmountUSD: "20.00",
			TopupAmountUSD: "50.00", AvailableUSD: "30.9000", BonusReversedUSD: "0.60", At: at})
	}},
	{"refund-declined", func(m *Mailer, to string) error {
		return m.RefundDeclined(context.Background(), to, RefundDecline{RequestID: "rr_01J9ZK3Q8V6Y2N4M7P5R1T0WXS",
			TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", Message: "The credit from this top-up was used by jobs, so there is no unused balance to refund."})
	}},
	{"operator-notice", func(m *Mailer, to string) error {
		return m.Operator(context.Background(), to, OperatorNotice{Title: "dispute opened on tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS",
			Body:   "A card payment for a Unify Compute top-up was disputed.",
			Action: "Answer the dispute in the Stripe dashboard before its due date.",
			Facts:  [][2]string{{"Account", "7"}, {"Dispute", "dp_1\nInjected: header"}}})
	}},
}

func TestRefundMessagesMatchTheirGoldenFiles(t *testing.T) {
	for _, tc := range refundMessages {
		t.Run(tc.kind, func(t *testing.T) {
			c := &capture{}
			if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
				t.Fatal(err)
			}
			if c.kind != strings.TrimSuffix(tc.kind, "-bonus") || c.to != "ada@example.com" {
				t.Fatalf("delivered %q to %q", c.kind, c.to)
			}
			p := parse(t, c.raw)
			golden(t, tc.kind+".txt", p.text)
			golden(t, tc.kind+".html", p.html)
			if n := strings.Count(p.html, "#F5A623"); n != 0 && n != 2 {
				t.Errorf("amber appears %d times", n)
			}
			if !strings.Contains(p.text, "contactus@felixsphere.com") && tc.kind != "operator-notice" {
				t.Error("no contact address")
			}
		})
	}
}

func TestRefundIssuedStatesThePolicy(t *testing.T) {
	c := &capture{}
	if err := refundMessages[0].send(testMailer(c), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if !strings.Contains(p.text, "Unused prepaid balance can be refunded on request; compute already used is not refunded.") {
		t.Errorf("policy missing:\n%s", p.text)
	}
}

func TestOperatorNoticeFlattensValues(t *testing.T) {
	c := &capture{}
	if err := refundMessages[len(refundMessages)-1].send(testMailer(c), "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if !strings.Contains(p.text, "Dispute: dp_1 Injected: header") {
		t.Errorf("a line break in a fact started a new line:\n%s", p.text)
	}
}
