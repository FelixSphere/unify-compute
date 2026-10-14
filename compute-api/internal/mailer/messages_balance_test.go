package mailer

import (
	"context"
	"strings"
	"testing"
)

func TestLowBalanceMatchesItsGoldenFiles(t *testing.T) {
	c := &capture{}
	m := testMailer(c)
	if err := m.LowBalance(context.Background(), "ada@example.com",
		LowBalance{AvailableUSD: "4.2000", HeldUSD: "15.8000", ThresholdUSD: "5.00"}); err != nil {
		t.Fatal(err)
	}
	if c.kind != KindLowBalance || c.to != "ada@example.com" {
		t.Fatalf("delivered %q to %q", c.kind, c.to)
	}
	p := parse(t, c.raw)
	golden(t, "low-balance.txt", p.text)
	golden(t, "low-balance.html", p.html)

	if n := strings.Count(p.html, "#F5A623"); n != 2 {
		t.Errorf("amber appears %d times; exactly the one button and its VML twin", n)
	}
	if want := m.AppOrigin + "/billing\n"; !strings.Contains(p.text, want) {
		t.Errorf("the plain text does not link to the Billing page (%q)", want)
	}
	if strings.Contains(p.text, "{{") || strings.Contains(p.html, "{{") || strings.Contains(p.text, "Unify API") {
		t.Error("an unfilled slot, or a pointer to the Unify API console")
	}
}
