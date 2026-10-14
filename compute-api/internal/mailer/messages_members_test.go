package mailer

import (
	"context"
	"mime"
	"strings"
	"testing"
)

// The member messages, with the arguments their golden files were made from.
var memberMessages = []struct {
	kind string
	send func(m *Mailer, to string) error
}{
	{"invitation", func(m *Mailer, to string) error {
		return m.Invitation(context.Background(), to, Invite{AccountName: "Acme Research", InviterEmail: "grace@example.com",
			Role: "member", Token: "q7Vb2xKp9LmN4rT8wYz1aBcDeFgHiJkL0MnOpQrStUv", ExpiresAt: at.AddDate(0, 0, 7)})
	}},
	{"invitation-accepted", func(m *Mailer, to string) error {
		return m.InvitationAccepted(context.Background(), to, Joined{Email: "ada@example.com", AccountName: "Acme Research", Role: "admin", At: at})
	}},
	{"member-removed", func(m *Mailer, to string) error {
		return m.RemovedFromAccount(context.Background(), to, Removal{AccountName: "Acme Research", ByEmail: "grace@example.com", At: at})
	}},
}

func TestMemberMessagesMatchTheirGoldenFiles(t *testing.T) {
	for _, tc := range memberMessages {
		t.Run(tc.kind, func(t *testing.T) {
			c := &capture{}
			if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
				t.Fatal(err)
			}
			if c.kind != tc.kind {
				t.Fatalf("kind %q", c.kind)
			}
			p := parse(t, c.raw)
			golden(t, tc.kind+".txt", p.text)
			golden(t, tc.kind+".html", p.html)
			if n := strings.Count(p.html, "#F5A623"); n != 0 && n != 2 {
				t.Errorf("amber appears %d times", n)
			}
			if len(imgSrc.FindAllStringSubmatch(p.html, -1)) != 2 {
				t.Error("the layout has exactly two images")
			}
			if strings.Contains(p.html, "{{") || strings.Contains(p.text, "{{") {
				t.Error("an unfilled slot")
			}
		})
	}
}

func TestMemberSubjectsFollowTheSpec(t *testing.T) {
	want := map[string]string{
		"invitation":          "grace@example.com invited you to Acme Research on Unify Compute",
		"invitation-accepted": "ada@example.com joined Acme Research on Unify Compute",
		"member-removed":      "You were removed from Acme Research on Unify Compute",
	}
	for _, tc := range memberMessages {
		c := &capture{}
		if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
			t.Fatal(err)
		}
		got, err := new(mime.WordDecoder).DecodeHeader(parse(t, c.raw).header.Get("Subject"))
		if err != nil || got != want[tc.kind] {
			t.Errorf("%s subject %q, want %q", tc.kind, got, want[tc.kind])
		}
	}
}

func TestInvitationTokenIsInTheFragment(t *testing.T) {
	c := &capture{}
	if err := memberMessages[0].send(testMailer(c), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if !strings.Contains(p.text, origin+"/accept-invitation#token=q7Vb2xKp9LmN4rT8wYz1aBcDeFgHiJkL0MnOpQrStUv") {
		t.Errorf("link not in the fragment:\n%s", p.text)
	}
	if strings.Contains(p.text, "?token=") || strings.Contains(p.html, "?token=") {
		t.Error("token in a query string")
	}
}

// An account name is chosen by a customer and lands in subjects and the
// plain-text part of mail to other people.
func TestAccountNameCannotInject(t *testing.T) {
	c := &capture{}
	m := testMailer(c)
	err := m.Invitation(context.Background(), "ada@example.com", Invite{AccountName: "Acme\r\nBcc: victim@example.com\n\nClick http://evil",
		InviterEmail: "grace@example.com", Role: "member", Token: "t", ExpiresAt: at})
	if err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if p.header.Get("Bcc") != "" {
		t.Fatal("header injected")
	}
	if strings.Contains(p.text, "\nBcc:") || strings.Contains(p.text, "\nClick http://evil") {
		t.Errorf("line break survived in the text part:\n%s", p.text)
	}
}

func TestSelfRemovalNamesNobody(t *testing.T) {
	c := &capture{}
	if err := testMailer(c).RemovedFromAccount(context.Background(), "ada@example.com", Removal{AccountName: "Acme", At: at}); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if strings.Contains(p.text, "Removed by") || !strings.Contains(p.text, "You left Acme") {
		t.Errorf("self-removal text:\n%s", p.text)
	}
}
