package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

func (f *fixture) otherSessions(s session) int {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodGet, "/sessions", s, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET /sessions: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		OtherSessions *int `json:"other_sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.OtherSessions == nil {
		f.t.Fatalf("GET /sessions body: %s", rec.Body)
	}
	return *body.OtherSessions
}

func (f *fixture) loggedIn(email string) session {
	f.t.Helper()
	rec := f.login(email, goodPass)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("login %d: %s", rec.Code, rec.Body)
	}
	return sessionFrom(f.t, rec)
}

func (f *fixture) meStatus(s session) int {
	f.t.Helper()
	return f.do(f.sessionReq(http.MethodGet, "/me", s, nil)).Code
}

func TestEndOtherSessionsKeepsThisOneAndOnlyTouchesTheUser(t *testing.T) {
	f := newFixture(t)
	here := f.activeUser("sessions@example.com")
	phone := f.loggedIn("sessions@example.com")
	laptop := f.loggedIn("sessions@example.com")
	stranger := f.activeUser("stranger@example.com")

	if n := f.otherSessions(here); n != 2 {
		t.Fatalf("other sessions %d, want 2", n)
	}
	if n := f.otherSessions(stranger); n != 0 {
		t.Fatalf("stranger sees %d other sessions, want 0", n)
	}

	rec := f.do(f.sessionReq(http.MethodPost, "/sessions/end-others", here, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("end others: %d %s", rec.Code, rec.Body)
	}
	var ended struct {
		Ended *int `json:"ended"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ended); err != nil || ended.Ended == nil || *ended.Ended != 2 {
		t.Fatalf("end others body: %s", rec.Body)
	}
	if c := sessionCookie(t, rec); c != nil {
		t.Fatalf("this session's cookie was touched: %v", c)
	}

	if code := f.meStatus(here); code != http.StatusOK {
		t.Fatalf("this session /me %d, want 200", code)
	}
	for name, s := range map[string]session{"phone": phone, "laptop": laptop} {
		if code := f.meStatus(s); code != http.StatusUnauthorized {
			t.Fatalf("%s /me %d after end-others, want 401", name, code)
		}
	}
	if code := f.meStatus(stranger); code != http.StatusOK {
		t.Fatalf("another user's session /me %d, want 200", code)
	}
	if n := f.otherSessions(here); n != 0 {
		t.Fatalf("other sessions after end-others %d, want 0", n)
	}
	if acts := f.auditActions(f.accountOf("sessions@example.com")); !slices.Contains(acts, accounts.ActionSessionsEnded) {
		t.Fatalf("audit %v has no %s", acts, accounts.ActionSessionsEnded)
	}
	if acts := f.auditActions(f.accountOf("stranger@example.com")); slices.Contains(acts, accounts.ActionSessionsEnded) {
		t.Fatalf("stranger's audit %v records %s", acts, accounts.ActionSessionsEnded)
	}

	// Nothing left to end is still a 200.
	rec = f.do(f.sessionReq(http.MethodPost, "/sessions/end-others", here, nil))
	if rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Fatalf("second end others: %d %s", rec.Code, rec.Body)
	}
}

// An idle session is already unusable, so it is neither shown nor counted as
// ended; it is still deleted.
func TestOtherSessionsIgnoresIdleSessions(t *testing.T) {
	f := newFixture(t)
	here := f.activeUser("idle@example.com")
	stale := f.loggedIn("idle@example.com")
	f.loggedIn("idle@example.com")

	sum := sha256.Sum256([]byte(stale.cookie))
	if _, err := f.db.Exec(context.Background(), `UPDATE sessions SET last_seen_at = $2 WHERE id_hash = $1`,
		sum[:], f.clock.now().Add(-accounts.SessionIdle-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := f.otherSessions(here); n != 1 {
		t.Fatalf("other sessions %d, want 1 (the idle one excluded)", n)
	}
	rec := f.do(f.sessionReq(http.MethodPost, "/sessions/end-others", here, nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ended":1}` {
		t.Fatalf("end others: %d %q", rec.Code, rec.Body)
	}
	var left int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM sessions`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("%d session rows left, want only this one", left)
	}
}

func TestEndOtherSessionsNeedsSessionAndCSRF(t *testing.T) {
	f := newFixture(t)
	here := f.activeUser("csrf@example.com")
	other := f.loggedIn("csrf@example.com")

	wantErr(t, f.do(req{method: http.MethodPost, path: "/sessions/end-others", cookie: here.cookie}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/sessions/end-others", cookie: here.cookie, csrf: other.csrf}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/sessions/end-others", cookie: here.cookie, csrf: here.csrf, origin: strp("https://evil.example")}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/sessions/end-others"}), http.StatusUnauthorized, "authentication_error", "session_required")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/sessions"}), http.StatusUnauthorized, "authentication_error", "session_required")

	if code := f.meStatus(other); code != http.StatusOK {
		t.Fatalf("a refused request ended a session: /me %d", code)
	}
}
