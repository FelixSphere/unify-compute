package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/support"
)

// The support conversation's customer side (ADR-0014). The operator's side
// is `admin support`, covered in internal/admin.

func (f *fixture) withSupport() *support.Service {
	s := support.New(f.db, f.h.mail, "ops@example.com", f.clock.now)
	f.h.support = s
	return s
}

func supportOf(t *testing.T, rec *httptest.ResponseRecorder, status int) supportView {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	var v supportView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("not a conversation: %s", rec.Body)
	}
	return v
}

func TestSupportRoutesAreNotImplementedWithoutSupport(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/support/conversations"}, {http.MethodPost, "/support/conversations"},
		{http.MethodGet, "/support/conversations/sc_x"}, {http.MethodPost, "/support/conversations/sc_x/messages"},
		{http.MethodPost, "/support/conversations/sc_x/close"},
	} {
		rec := f.do(req{method: rt.method, path: rt.path, cookie: s.cookie, csrf: s.csrf, body: map[string]string{"message": "hi"}})
		wantErr(t, rec, http.StatusNotImplemented, "not_implemented", "not_implemented")
	}
}

func TestSupportConversationFromTheDashboard(t *testing.T) {
	f := newFixture(t)
	svc := f.withSupport()
	s := f.activeUser("ada@example.com")
	start := func(msg string) *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodPost, path: "/support/conversations", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"message": msg}})
	}

	wantErr(t, f.do(req{method: http.MethodGet, path: "/support/conversations"}), http.StatusUnauthorized, "authentication_error", "session_required")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/support/conversations", cookie: s.cookie, body: map[string]string{"message": "hi"}}),
		http.StatusForbidden, "permission_error", "csrf_failed")
	if e := wantErr(t, start("   "), http.StatusBadRequest, "invalid_request", "invalid_message"); e.Error.Param != "message" {
		t.Fatalf("param %q", e.Error.Param)
	}
	wantErr(t, start(strings.Repeat("x", support.MaxBody+1)), http.StatusBadRequest, "invalid_request", "invalid_message")

	c := supportOf(t, start("My job is stuck."), http.StatusCreated)
	if c.State != "open" || len(c.Messages) != 1 || c.Messages[0].Body != "My job is stuck." || c.Messages[0].FromStaff {
		t.Fatalf("started %+v", c)
	}
	wantErr(t, start("Another one."), http.StatusConflict, "conflict", "conversation_open")

	// The operator address is told once, when it starts waiting, and never
	// what the customer wrote.
	f.h.Wait()
	notices := f.mail.to("ops@example.com")
	if len(notices) != 1 || notices[0].kind != "operator-notice" || !strings.Contains(notices[0].text, c.ID) || strings.Contains(notices[0].text, "stuck") {
		t.Fatalf("operator notices %+v", notices)
	}

	path := "/support/conversations/" + c.ID
	write := func(msg string) *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodPost, path: path + "/messages", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"message": msg}})
	}
	if rec := write("Any news?"); rec.Code != http.StatusCreated {
		t.Fatalf("write %d: %s", rec.Code, rec.Body)
	}
	f.h.Wait()
	if n := len(f.mail.to("ops@example.com")); n != 1 {
		t.Fatalf("a second message while waiting told the operator again: %d notices", n)
	}

	// Staff answer (the admin CLI's call): the customer is emailed and sees it.
	if _, mailed, err := svc.Reply(t.Context(), c.ID, "ops-alice", "Looking now.", noAudit); err != nil || !mailed {
		t.Fatalf("reply mailed=%v %v", mailed, err)
	}
	if got := f.mail.to("ada@example.com"); got[len(got)-1].kind != "support-reply" || strings.Contains(got[len(got)-1].text, "Looking now.") {
		t.Fatalf("reply mail %+v", got[len(got)-1])
	}
	got := supportOf(t, f.do(req{method: http.MethodGet, path: path, cookie: s.cookie}), http.StatusOK)
	if got.State != "answered" || len(got.Messages) != 3 || !got.Messages[2].FromStaff || got.Messages[2].Body != "Looking now." {
		t.Fatalf("after reply %+v", got)
	}
	if strings.Contains(f.do(req{method: http.MethodGet, path: path, cookie: s.cookie}).Body.String(), "ops-alice") {
		t.Fatal("the customer sees who answered")
	}

	// Writing back waits again and tells the operator again.
	if rec := write("Still stuck."); rec.Code != http.StatusCreated {
		t.Fatalf("write %d: %s", rec.Code, rec.Body)
	}
	f.h.Wait()
	if n := len(f.mail.to("ops@example.com")); n != 2 {
		t.Fatalf("%d operator notices, want 2", n)
	}

	// Another user, even of the same deployment, sees none of it.
	other := f.activeUser("bob@example.com")
	wantErr(t, f.do(req{method: http.MethodGet, path: path, cookie: other.cookie}), http.StatusNotFound, "not_found", "not_found")
	wantErr(t, f.do(req{method: http.MethodPost, path: path + "/messages", cookie: other.cookie, csrf: other.csrf, body: map[string]string{"message": "x"}}),
		http.StatusNotFound, "not_found", "not_found")
	wantErr(t, f.do(req{method: http.MethodPost, path: path + "/close", cookie: other.cookie, csrf: other.csrf}), http.StatusNotFound, "not_found", "not_found")

	closed := supportOf(t, f.do(req{method: http.MethodPost, path: path + "/close", cookie: s.cookie, csrf: s.csrf}), http.StatusOK)
	if closed.State != "closed" || closed.ClosedAt == nil {
		t.Fatalf("closed %+v", closed)
	}
	wantErr(t, write("hello?"), http.StatusConflict, "conflict", "conversation_closed")
	f.clock.advance(time.Minute)
	next := supportOf(t, start("A new question."), http.StatusCreated)

	var list struct {
		Data []supportView `json:"data"`
	}
	rec := f.do(req{method: http.MethodGet, path: "/support/conversations", cookie: s.cookie})
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 2 || list.Data[0].ID != next.ID || list.Data[1].ID != c.ID ||
		list.Data[0].Preview != "A new question." || len(list.Data[0].Messages) != 0 {
		t.Fatalf("list %s", rec.Body)
	}
}

func TestSupportWritesAreRateLimitedPerUser(t *testing.T) {
	f := newFixture(t)
	f.withSupport()
	s := f.activeUser("ada@example.com")
	rec := f.do(req{method: http.MethodPost, path: "/support/conversations", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"message": "1"}})
	c := supportOf(t, rec, http.StatusCreated)
	write := func() *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodPost, path: "/support/conversations/" + c.ID + "/messages", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"message": "more"}})
	}
	for i := 1; i < supportWritesPerUser.Limit; i++ {
		if rec := write(); rec.Code != http.StatusCreated {
			t.Fatalf("write %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	rec = write()
	wantErr(t, rec, http.StatusTooManyRequests, "rate_limited", "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// Someone else's budget is their own.
	other := f.activeUser("bob@example.com")
	if rec := f.do(req{method: http.MethodPost, path: "/support/conversations", cookie: other.cookie, csrf: other.csrf, body: map[string]string{"message": "hi"}}); rec.Code != http.StatusCreated {
		t.Fatalf("other user: %d %s", rec.Code, rec.Body)
	}
}

func noAudit(context.Context, pgx.Tx, int64, string, map[string]any) error { return nil }
