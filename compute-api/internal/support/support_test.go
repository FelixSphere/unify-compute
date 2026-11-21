package support_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/support"
)

var ctx = context.Background()

type mailbox struct {
	replies, notices []string
	fail             error
}

func (m *mailbox) SupportReply(_ context.Context, to string, a mailer.SupportAnswer) error {
	if m.fail != nil {
		return m.fail
	}
	m.replies = append(m.replies, to+" "+a.ConversationID)
	return nil
}

func (m *mailbox) Operator(_ context.Context, to string, n mailer.OperatorNotice) error {
	m.notices = append(m.notices, to+" "+n.Facts[0][1])
	return nil
}

type env struct {
	t      *testing.T
	db     *pgxpool.Pool
	svc    *support.Service
	mail   *mailbox
	audits []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	e := &env{t: t, db: st.Pool(), mail: &mailbox{}}
	// Each call is a second later, so order is observable.
	tick := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	e.svc = support.New(st.Pool(), e.mail, "ops@example.com", func() time.Time { tick = tick.Add(time.Second); return tick })
	return e
}

func (e *env) user(email, status string) accounts.User {
	e.t.Helper()
	u := accounts.User{Email: email}
	if err := e.db.QueryRow(ctx, `INSERT INTO accounts (created_at) VALUES (now()) RETURNING id`).Scan(&u.AccountID); err != nil {
		e.t.Fatal(err)
	}
	if err := e.db.QueryRow(ctx, `INSERT INTO users (account_id, email, password_hash, status, email_verified_at, created_at)
		VALUES ($1, $2, '$argon2id$x', $3, now(), now()) RETURNING id`, u.AccountID, email, status).Scan(&u.ID); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) audit(_ context.Context, _ pgx.Tx, accountID int64, action string, detail map[string]any) error {
	e.audits = append(e.audits, action+" "+detail["conversation_id"].(string))
	return nil
}

func TestConversationGoesBackAndForth(t *testing.T) {
	e := newEnv(t)
	ada := e.user("ada@example.com", "active")

	for _, bad := range []string{"", "  \n ", strings.Repeat("x", support.MaxBody+1)} {
		if _, err := e.svc.Start(ctx, ada, bad); !errors.Is(err, support.ErrEmpty) && !errors.Is(err, support.ErrTooBig) {
			t.Fatalf("Start(%d chars): %v", len(bad), err)
		}
	}
	c, err := e.svc.Start(ctx, ada, "  My job is stuck.\r\nSecond line.  ")
	if err != nil {
		t.Fatal(err)
	}
	if c.State != support.Open || len(c.Messages) != 1 || c.Messages[0].Body != "My job is stuck.\nSecond line." || c.Messages[0].FromStaff {
		t.Fatalf("started %+v", c)
	}
	if _, err := e.svc.Start(ctx, ada, "again"); !errors.Is(err, support.ErrLive) {
		t.Fatalf("second live conversation: %v", err)
	}
	// Still waiting: a second message does not announce it again.
	if _, waiting, err := e.svc.Write(ctx, ada, c.ID, "Any news?"); err != nil || waiting {
		t.Fatalf("write while open: waiting=%v %v", waiting, err)
	}
	if w, err := e.svc.Waiting(ctx); err != nil || len(w) != 1 || w[0].ID != c.ID || w[0].Preview != "Any news?" {
		t.Fatalf("waiting %+v %v", w, err)
	}

	m, mailed, err := e.svc.Reply(ctx, c.ID, "ops-alice", "Looking at it now.", e.audit)
	if err != nil || !mailed || !m.FromStaff {
		t.Fatalf("reply %+v mailed=%v %v", m, mailed, err)
	}
	if len(e.mail.replies) != 1 || e.mail.replies[0] != "ada@example.com "+c.ID {
		t.Fatalf("reply mail %v", e.mail.replies)
	}
	if len(e.audits) != 1 || e.audits[0] != accounts.ActionSupportReplied+" "+c.ID {
		t.Fatalf("audits %v", e.audits)
	}
	if w, _ := e.svc.Waiting(ctx); len(w) != 0 {
		t.Fatalf("an answered conversation still waits: %+v", w)
	}
	got, err := e.svc.Get(ctx, ada.ID, c.ID, 0)
	if err != nil || got.State != support.Answered || len(got.Messages) != 3 || got.LastMessageID != m.ID {
		t.Fatalf("after reply %+v %v", got, err)
	}
	// "after" is how the operator's tooling reads only what is new.
	if tail, _ := e.svc.Get(ctx, 0, c.ID, got.Messages[1].ID); len(tail.Messages) != 1 || tail.Messages[0].ID != m.ID {
		t.Fatalf("after: %+v", tail.Messages)
	}

	// The customer answers: it waits again, and this time it is announced.
	if _, waiting, err := e.svc.Write(ctx, ada, c.ID, "Thanks, still stuck."); err != nil || !waiting {
		t.Fatalf("write after answer: waiting=%v %v", waiting, err)
	}
	if got, _ := e.svc.Get(ctx, ada.ID, c.ID, 0); got.State != support.Open {
		t.Fatalf("state %s", got.State)
	}

	closed, err := e.svc.Close(ctx, ada.ID, c.ID)
	if err != nil || closed.State != support.Closed || closed.ClosedAt == nil {
		t.Fatalf("close %+v %v", closed, err)
	}
	if _, err := e.svc.Close(ctx, ada.ID, c.ID); err != nil {
		t.Fatalf("closing twice: %v", err)
	}
	if _, _, err := e.svc.Write(ctx, ada, c.ID, "hello?"); !errors.Is(err, support.ErrClosed) {
		t.Fatalf("write to closed: %v", err)
	}
	if _, _, err := e.svc.Reply(ctx, c.ID, "ops-alice", "late", e.audit); !errors.Is(err, support.ErrClosed) {
		t.Fatalf("reply to closed: %v", err)
	}
	// A closed conversation is history; the next message starts a new one.
	next, err := e.svc.Start(ctx, ada, "A new question.")
	if err != nil || next.ID == c.ID {
		t.Fatalf("new conversation %+v %v", next, err)
	}
	if list, _ := e.svc.ByUser(ctx, ada.ID); len(list) != 2 || list[0].ID != next.ID {
		t.Fatalf("history %+v", list)
	}
}

func TestConversationIsItsUsersOnly(t *testing.T) {
	e := newEnv(t)
	ada, bob := e.user("ada@example.com", "active"), e.user("bob@example.com", "active")
	c, err := e.svc.Start(ctx, ada, "Mine.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, bob.ID, c.ID, 0); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, _, err := e.svc.Write(ctx, bob, c.ID, "Not mine."); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("write: %v", err)
	}
	if _, err := e.svc.Close(ctx, bob.ID, c.ID); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("close: %v", err)
	}
	if list, _ := e.svc.ByUser(ctx, bob.ID); len(list) != 0 {
		t.Fatalf("bob sees %+v", list)
	}
	if got, _ := e.svc.Get(ctx, ada.ID, c.ID, 0); got.State != support.Open || len(got.Messages) != 1 {
		t.Fatalf("ada's conversation changed: %+v", got)
	}
}

func TestReplyIsSavedWhenTheEmailFails(t *testing.T) {
	e := newEnv(t)
	ada := e.user("ada@example.com", "active")
	c, _ := e.svc.Start(ctx, ada, "Hello.")
	e.mail.fail = errors.New("ses: MessageRejected")
	if _, mailed, err := e.svc.Reply(ctx, c.ID, "ops", "Hi.", e.audit); err != nil || mailed {
		t.Fatalf("mailed=%v %v", mailed, err)
	}
	if got, _ := e.svc.Get(ctx, ada.ID, c.ID, 0); got.State != support.Answered || len(got.Messages) != 2 {
		t.Fatalf("reply not saved: %+v", got)
	}
	// A user who can no longer log in is not mailed.
	gone := e.user("gone@example.com", "disabled")
	g, _ := e.svc.Start(ctx, gone, "Hello.")
	e.mail.fail = nil
	if _, mailed, err := e.svc.Reply(ctx, g.ID, "ops", "Hi.", e.audit); err != nil || mailed || len(e.mail.replies) != 0 {
		t.Fatalf("disabled user: mailed=%v %v %v", mailed, err, e.mail.replies)
	}
}

func TestAdminCloseAndOperatorNotice(t *testing.T) {
	e := newEnv(t)
	ada := e.user("ada@example.com", "active")
	c, _ := e.svc.Start(ctx, ada, "Hello.")
	if err := e.svc.TellOperator(ctx, c); err != nil || len(e.mail.notices) != 1 || e.mail.notices[0] != "ops@example.com "+c.ID {
		t.Fatalf("notice %v %v", e.mail.notices, err)
	}
	if err := e.svc.AdminClose(ctx, c.ID, "ops-alice", e.audit); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AdminClose(ctx, c.ID, "ops-alice", e.audit); !errors.Is(err, support.ErrClosed) {
		t.Fatalf("second admin close: %v", err)
	}
	if err := e.svc.AdminClose(ctx, "sc_00000000000000000000000000", "ops-alice", e.audit); !errors.Is(err, support.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	var by string
	if err := e.db.QueryRow(ctx, `SELECT closed_by FROM support_conversations WHERE id = $1`, c.ID).Scan(&by); err != nil || by != "ops-alice" {
		t.Fatalf("closed_by %q %v", by, err)
	}
	if len(e.audits) != 1 || e.audits[0] != accounts.ActionSupportClosed+" "+c.ID {
		t.Fatalf("audits %v", e.audits)
	}
}
