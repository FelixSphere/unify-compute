package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

type keyBody struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Display    string   `json:"display"`
	Scopes     []string `json:"scopes"`
	CreatedAt  int64    `json:"created_at"`
	ExpiresAt  *int64   `json:"expires_at"`
	LastUsedAt *int64   `json:"last_used_at"`
	RevokedAt  *int64   `json:"revoked_at"`
	Secret     string   `json:"secret"`
}

func (f *fixture) sessionReq(method, path string, s session, body any) req {
	return req{method: method, path: path, body: body, cookie: s.cookie, csrf: s.csrf}
}

func (f *fixture) createKey(s session, body any) keyBody {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodPost, "/keys", s, body))
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create key: %d %s", rec.Code, rec.Body)
	}
	var k keyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		f.t.Fatal(err)
	}
	return k
}

func TestCreateKeyShowsTheSecretOnceAndEmails(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("keys@example.com")
	acct := f.accountOf("keys@example.com")

	expires := f.clock.now().Add(30 * 24 * time.Hour).Unix()
	k := f.createKey(s, map[string]any{"name": "CI runner", "scopes": []string{"read", "jobs"}, "expires_at": expires})
	if !accounts.WellFormedKey(k.Secret) || k.Display != k.Secret[:12] || k.Name != "CI runner" ||
		fmt.Sprint(k.Scopes) != "[jobs read]" || k.ExpiresAt == nil || *k.ExpiresAt != expires || k.RevokedAt != nil ||
		k.CreatedAt != f.clock.now().Unix() {
		t.Fatalf("created %+v", k)
	}

	rec := f.do(f.sessionReq(http.MethodGet, "/keys", s, nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), k.Secret[12:]) || strings.Contains(rec.Body.String(), `"secret"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list []keyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].ID != k.ID {
		t.Fatalf("list %s (%v)", rec.Body, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "[") {
		t.Fatalf("list is not a bare array: %s", rec.Body)
	}

	f.h.Wait()
	msgs := f.mail.to("keys@example.com")
	var created []sent
	for _, m := range msgs {
		if strings.Contains(m.text, k.Secret[len(accounts.KeyPrefix):]) {
			t.Fatalf("secret emailed in %s", m.kind)
		}
		if m.kind == "key-created" {
			created = append(created, m)
		}
	}
	if len(created) != 1 || !strings.Contains(created[0].text, k.Display) || !strings.Contains(created[0].text, "CI runner") ||
		!strings.Contains(created[0].text, "jobs, read") {
		t.Fatalf("key-created email %+v", created)
	}
	if got := f.auditActions(acct); got[len(got)-1] != accounts.ActionKeyCreated {
		t.Fatalf("audit %v", got)
	}
}

func TestCreateKeyNeedsAFreshReauth(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("stale@example.com")
	body := map[string]any{"name": "k", "scopes": []string{"jobs"}}

	f.clock.advance(accounts.ReauthWindow)
	e := wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, body)), 401, "authentication_error", "reauth_required")
	if e.Error.Param != "" {
		t.Errorf("param %q", e.Error.Param)
	}
	// Checked before the body, so even a malformed one learns reauth first.
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, "nope")), 401, "authentication_error", "reauth_required")

	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/reauth", s, map[string]string{"password": "wrong password!"})), 401, "authentication_error", "invalid_credentials")
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, body)), 401, "authentication_error", "reauth_required")
	if rec := f.do(f.sessionReq(http.MethodPost, "/reauth", s, map[string]string{"password": goodPass})); rec.Code != http.StatusNoContent {
		t.Fatalf("reauth %d %s", rec.Code, rec.Body)
	}
	f.createKey(s, body)
	f.h.Wait()
	if n := len(f.mail.to("stale@example.com")); n != 2 { // verification code, key created
		t.Fatalf("%d emails", n)
	}
}

func TestCreateKeyValidation(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("valid@example.com")
	for _, c := range []struct {
		body        any
		code, param string
	}{
		{"not json", "malformed_body", ""},
		{map[string]any{"name": "", "scopes": []string{"jobs"}}, "invalid_name", "name"},
		{map[string]any{"name": strings.Repeat("n", 65), "scopes": []string{"jobs"}}, "invalid_name", "name"},
		{map[string]any{"name": "evil\r\nBcc: victim@example.com", "scopes": []string{"jobs"}}, "invalid_name", "name"},
		{map[string]any{"name": "<img src=x onerror=alert(1)>", "scopes": []string{"jobs"}}, "invalid_name", "name"},
		{map[string]any{"name": "k", "scopes": []string{}}, "invalid_scope", "scopes"},
		{map[string]any{"name": "k"}, "invalid_scope", "scopes"},
		{map[string]any{"name": "k", "scopes": []string{"billing"}}, "invalid_scope", "scopes"},
		{map[string]any{"name": "k", "scopes": []string{"jobs"}, "expires_at": f.clock.now().Unix()}, "invalid_expires_at", "expires_at"},
	} {
		e := wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, c.body)), 400, "invalid_request", c.code)
		if e.Error.Param != c.param {
			t.Errorf("%v: param %q, want %q", c.body, e.Error.Param, c.param)
		}
	}
	f.h.Wait()
	for _, m := range f.mail.to("valid@example.com") {
		if m.kind == "key-created" {
			t.Fatal("a refused creation sent the email")
		}
	}
}

func TestKeyLimitAndCreationThrottle(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("limit@example.com")
	var first keyBody
	for i := range accounts.MaxActiveKeys {
		k := f.createKey(s, map[string]any{"name": fmt.Sprintf("k%d", i), "scopes": []string{"jobs"}})
		if i == 0 {
			first = k
		}
	}
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, map[string]any{"name": "eleventh", "scopes": []string{"jobs"}})),
		403, "permission_error", "key_limit_reached")

	// Revoke-and-recreate frees a slot, until the hourly creation limit.
	// Every well-formed attempt counts, the refused eleventh included.
	for i := 0; ; i++ {
		del := f.do(f.sessionReq(http.MethodDelete, "/keys/"+strconv.FormatInt(first.ID, 10), s, nil))
		if del.Code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", del.Code, del.Body)
		}
		rec := f.do(f.sessionReq(http.MethodPost, "/keys", s, map[string]any{"name": "again", "scopes": []string{"jobs"}}))
		if rec.Code == http.StatusTooManyRequests {
			if rec.Header().Get("Retry-After") == "" || accounts.MaxActiveKeys+1+i != keyCreatePerAccount.Limit {
				t.Fatalf("throttled after %d attempts: %s", accounts.MaxActiveKeys+1+i, rec.Header())
			}
			break
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("recreate: %d %s", rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
			t.Fatal(err)
		}
		if i > keyCreatePerAccount.Limit {
			t.Fatal("never throttled")
		}
	}
}

func TestRevokeKey(t *testing.T) {
	f := newFixture(t)
	alice := f.activeUser("alice@example.com")
	bob := f.activeUser("bob@example.com")
	k := f.createKey(alice, map[string]any{"name": "k", "scopes": []string{"jobs"}})
	path := "/keys/" + strconv.FormatInt(k.ID, 10)

	wantErr(t, f.do(f.sessionReq(http.MethodDelete, path, bob, nil)), 404, "not_found", "not_found")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/keys/abc", alice, nil)), 404, "not_found", "not_found")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/keys/999999", alice, nil)), 404, "not_found", "not_found")
	wantErr(t, f.do(req{method: http.MethodDelete, path: path, cookie: alice.cookie}), 403, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodDelete, path: path}), 401, "authentication_error", "session_required")

	f.clock.advance(time.Minute)
	for range 2 {
		if rec := f.do(f.sessionReq(http.MethodDelete, path, alice, nil)); rec.Code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
		}
	}
	rec := f.do(f.sessionReq(http.MethodGet, "/keys", alice, nil))
	var list []keyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].RevokedAt == nil || *list[0].RevokedAt != f.clock.now().Unix() {
		t.Fatalf("after revoke %s", rec.Body)
	}
	// One audit row for the revoke that changed something.
	n := 0
	for _, a := range f.auditActions(f.accountOf("alice@example.com")) {
		if a == accounts.ActionKeyRevoked {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d key_revoked audit rows", n)
	}
}

func TestSuspendedAccountCannotCreateKeys(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("susp@example.com")
	if _, err := f.db.Exec(t.Context(), `UPDATE accounts SET status = 'suspended', suspended_reason = 'operator' WHERE id = $1`,
		f.accountOf("susp@example.com")); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/keys", s, map[string]any{"name": "k", "scopes": []string{"jobs"}})),
		403, "authentication_error", "account_suspended")
	if rec := f.do(f.sessionReq(http.MethodGet, "/keys", s, nil)); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("list while suspended: %d %s", rec.Code, rec.Body)
	}
}

func TestKeyRoutesNeedTheAppHostAndASession(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("host-keys@example.com")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/keys", host: "api.compute.test", cookie: s.cookie}), 404, "not_found", "not_found")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/keys"}), 401, "authentication_error", "session_required")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/keys", cookie: s.cookie, csrf: s.csrf, origin: strp("https://evil.example"),
		body: map[string]any{"name": "k", "scopes": []string{"jobs"}}}), 403, "permission_error", "csrf_failed")
}
