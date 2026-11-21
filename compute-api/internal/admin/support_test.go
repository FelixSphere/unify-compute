package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/support"
)

// `admin support list|show|reply|close` (ADR-0014). The customer's side is
// covered in internal/dashboard.

type supportMail struct {
	replies []string
	fail    error
}

func (m *supportMail) SupportReply(_ context.Context, to string, _ mailer.SupportAnswer) error {
	if m.fail != nil {
		return m.fail
	}
	m.replies = append(m.replies, to)
	return nil
}

func (m *supportMail) Operator(context.Context, string, mailer.OperatorNotice) error { return nil }

type supportEnv struct {
	*env
	svc  *support.Service
	mail *supportMail
}

func newSupportEnv(t *testing.T) *supportEnv {
	t.Helper()
	e := newEnv(t)
	m := &supportMail{}
	return &supportEnv{env: e, svc: support.New(e.db, m, "", nil), mail: m}
}

func (e *supportEnv) support(stdin string, args ...string) error {
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Support: e.svc, In: strings.NewReader(stdin), Actor: "ops-alice", Out: &e.out}
	return cli.Run(ctx, append([]string{"support"}, args...))
}

func (e *supportEnv) customer(email string) accounts.User {
	e.t.Helper()
	e.signup(email, true)
	u, err := e.acc.UserByEmail(ctx, email)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func TestSupportNeedsLocalAccounts(t *testing.T) {
	e := newEnv(t)
	if err := e.run("support", "list"); !errors.Is(err, admin.ErrUsage) || !strings.Contains(err.Error(), "no support conversations") {
		t.Fatalf("%v", err)
	}
}

func TestSupportListShowReplyClose(t *testing.T) {
	e := newSupportEnv(t)
	if err := e.support("", "list"); err != nil || !strings.Contains(e.out.String(), "no conversation is waiting") {
		t.Fatalf("empty list: %v %q", err, e.out.String())
	}
	ada := e.customer("ada@example.com")
	c, err := e.svc.Start(ctx, ada, "My job is stuck.")
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{},
		{"answer", c.ID},
		{"show"},
		{"show", "tu_1"},
		{"reply", c.ID},
		{"reply", c.ID, "--message", "x", "--stdin"},
		{"reply", c.ID, "--message", "   "},
		{"reply", c.ID, "--message", "x", "extra"},
		{"reply", "sc_00000000000000000000000000", "--message", "x"},
		{"close", c.ID, "--reason", "r"},
		{"list", "--after", "3"},
	} {
		if err := e.support("", args...); !errors.Is(err, admin.ErrUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM support_messages`); n != 1 {
		t.Fatalf("a refused command wrote: %d messages", n)
	}

	// list says who waits without the address; --json carries no text either.
	if err := e.support("", "list"); err != nil || !strings.Contains(e.out.String(), c.ID+"  account ") || strings.Contains(e.out.String(), "ada@example.com") {
		t.Fatalf("list: %v %q", err, e.out.String())
	}
	if err := e.support("", "list", "--json"); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["id"] != c.ID || rows[0]["state"] != "open" ||
		rows[0]["last_message_id"].(float64) < 1 || strings.Contains(e.out.String(), "stuck") || strings.Contains(e.out.String(), "@") {
		t.Fatalf("list --json: %v %q", err, e.out.String())
	}

	if err := e.support("", "show", c.ID); err != nil || !strings.Contains(e.out.String(), "ada@example.com") || !strings.Contains(e.out.String(), "customer") ||
		!strings.Contains(e.out.String(), "My job is stuck.") {
		t.Fatalf("show: %v %q", err, e.out.String())
	}

	// The message arrives on stdin, so no shell ever quotes it.
	text := "Looking at it now.\nIt's the \"quota\" $HOME `x`."
	if err := e.support(text+"\n", "reply", c.ID, "--stdin"); err != nil || !strings.Contains(e.out.String(), "replied "+c.ID+" message ") {
		t.Fatalf("reply: %v %q", err, e.out.String())
	}
	if len(e.mail.replies) != 1 || e.mail.replies[0] != "ada@example.com" {
		t.Fatalf("mailed %v", e.mail.replies)
	}
	var body, author, state string
	if err := e.db.QueryRow(ctx, `SELECT m.body, m.author, c.state FROM support_messages m JOIN support_conversations c ON c.id = m.conversation_id
		WHERE m.from_staff`).Scan(&body, &author, &state); err != nil || body != text || author != "ops-alice" || state != "answered" {
		t.Fatalf("staff message %q by %q, state %s: %v", body, author, state, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'support_replied' AND actor_type = 'admin' AND actor_id = 'ops-alice'
		AND account_id = $1 AND detail->>'conversation_id' = $2`, ada.AccountID, c.ID); n != 1 {
		t.Fatalf("%d audit rows for the reply", n)
	}
	if err := e.support("", "list"); err != nil || !strings.Contains(e.out.String(), "no conversation is waiting") {
		t.Fatalf("an answered conversation still waits: %v %q", err, e.out.String())
	}

	// show --after N --json is what follows a conversation: only what is new.
	if err := e.support("", "show", c.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	var full struct {
		Email    string
		State    string
		Messages []struct {
			ID        int64
			FromStaff bool `json:"from_staff"`
			Body      string
		}
	}
	if err := json.Unmarshal(e.out.Bytes(), &full); err != nil || full.Email != "ada@example.com" || full.State != "answered" || len(full.Messages) != 2 ||
		!full.Messages[1].FromStaff {
		t.Fatalf("show --json: %v %q", err, e.out.String())
	}
	if err := e.support("", "show", c.ID, "--after", "1", "--json"); err != nil {
		t.Fatal(err)
	}
	full.Messages = nil
	if err := json.Unmarshal(e.out.Bytes(), &full); err != nil || len(full.Messages) != 1 || full.Messages[0].Body != text {
		t.Fatalf("show --after: %v %q", err, e.out.String())
	}

	// A reply whose email fails is saved, and the exit says the email did not go.
	e.mail.fail = errors.New("ses: MessageRejected")
	if err := e.support("", "reply", c.ID, "--message", "One more thing."); !errors.Is(err, admin.ErrReplyNotMailed) {
		t.Fatalf("reply with failing mail: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM support_messages WHERE from_staff`); n != 2 {
		t.Fatalf("%d staff messages", n)
	}

	if err := e.support("", "close", c.ID); err != nil || !strings.Contains(e.out.String(), "closed "+c.ID) {
		t.Fatalf("close: %v %q", err, e.out.String())
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'support_closed' AND detail->>'conversation_id' = $1`, c.ID); n != 1 {
		t.Fatalf("%d audit rows for the close", n)
	}
	for _, args := range [][]string{{"close", c.ID}, {"reply", c.ID, "--message", "late"}} {
		if err := e.support("", args...); err == nil || !strings.Contains(err.Error(), "closed") {
			t.Errorf("%v on a closed conversation: %v", args, err)
		}
	}
}
