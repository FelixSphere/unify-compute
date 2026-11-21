package mailer

import (
	"context"
	"strings"
	"testing"
)

// ADR-0014's message: a staff reply in a support conversation. Golden like
// the others; it must point at the Support page and never carry the reply.
func TestSupportReplyMatchesItsGoldenFiles(t *testing.T) {
	c := &capture{}
	if err := testMailer(c).SupportReply(context.Background(), "ada@example.com",
		SupportAnswer{ConversationID: "sc_01J9ZK3Q8V6Y2N4M7P5R1T0WXS", At: at}); err != nil {
		t.Fatal(err)
	}
	if c.kind != "support-reply" || c.to != "ada@example.com" {
		t.Fatalf("delivered %q to %q", c.kind, c.to)
	}
	p := parse(t, c.raw)
	golden(t, "support-reply.txt", p.text)
	golden(t, "support-reply.html", p.html)
	if !strings.Contains(p.text, origin+"/support") {
		t.Error("no link to the Support page")
	}
	if n := strings.Count(p.html, "#F5A623"); n != 2 {
		t.Errorf("amber appears %d times", n)
	}
}
