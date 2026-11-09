package mailer

import (
	"context"
	"strings"
	"testing"
)

// ADR-0013's messages: a refund that takes back bonus credit, and a declined
// refund request. Golden like the B2 messages in messages_refunds_test.go.
var bonusRefundMessages = []struct {
	name, kind string
	send       func(m *Mailer, to string) error
}{
	{"refund-issued-bonus", "refund-issued", func(m *Mailer, to string) error {
		return m.RefundIssued(context.Background(), to, Refund{TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", AmountUSD: "20.00",
			TopupAmountUSD: "50.00", AvailableUSD: "30.9000", BonusReversedUSD: "0.60", At: at})
	}},
	{"refund-declined", "refund-declined", func(m *Mailer, to string) error {
		return m.RefundDeclined(context.Background(), to, RefundDecline{RequestID: "rr_01J9ZK3Q8V6Y2N4M7P5R1T0WXS",
			TopupID: "tu_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", Message: "The credit from this top-up was used by jobs, so there is no unused balance to refund."})
	}},
}

func TestBonusRefundMessagesMatchTheirGoldenFiles(t *testing.T) {
	for _, tc := range bonusRefundMessages {
		t.Run(tc.name, func(t *testing.T) {
			c := &capture{}
			if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
				t.Fatal(err)
			}
			if c.kind != tc.kind || c.to != "ada@example.com" {
				t.Fatalf("delivered %q to %q", c.kind, c.to)
			}
			p := parse(t, c.raw)
			golden(t, tc.name+".txt", p.text)
			golden(t, tc.name+".html", p.html)
			if n := strings.Count(p.html, "#F5A623"); n != 0 && n != 2 {
				t.Errorf("amber appears %d times", n)
			}
			if !strings.Contains(p.text, "contactus@felixsphere.com") {
				t.Error("no contact address")
			}
		})
	}
}
