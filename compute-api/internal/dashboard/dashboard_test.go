package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
)

func TestNewChecksTheAppOrigin(t *testing.T) {
	cfg := func(o string) Config {
		return Config{Accounts: &accounts.Service{}, Mailer: &mailer.Mailer{}, AppOrigin: o, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	for _, o := range []string{"", "app.compute.unifyai.us", "http://app.compute.unifyai.us", "https://app.compute.unifyai.us/x",
		"https://app.compute.unifyai.us?q", "https://u@app.compute.unifyai.us", "ftp://app.compute.unifyai.us"} {
		if _, err := New(cfg(o)); err == nil {
			t.Errorf("%q accepted", o)
		}
	}
	for _, o := range []string{"https://app.compute.unifyai.us", "https://app.compute.unifyai.us/", "http://localhost:3000", "http://127.0.0.1:3000"} {
		if _, err := New(cfg(o)); err != nil {
			t.Errorf("%q refused: %v", o, err)
		}
	}
}

func TestClientIPIsTheLastForwardedEntry(t *testing.T) {
	cases := []struct {
		name   string
		xff    []string
		remote string
		want   string
	}{
		{"alb only", []string{"198.51.100.7"}, "10.0.0.5:4000", "198.51.100.7"},
		{"spoofed first entry", []string{"1.2.3.4, 198.51.100.7"}, "10.0.0.5:4000", "198.51.100.7"},
		{"two header lines", []string{"1.2.3.4", "5.6.7.8, 198.51.100.7"}, "10.0.0.5:4000", "198.51.100.7"},
		{"ipv6", []string{"x, 2001:db8::1"}, "10.0.0.5:4000", "2001:db8::1"},
		{"garbage last falls back to peer", []string{"198.51.100.7, nonsense"}, "10.0.0.5:4000", "10.0.0.5"},
		{"no header", nil, "192.0.2.1:5555", "192.0.2.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestConfig(t *testing.T) {
	f := newFixture(t)
	rec := f.do(req{method: http.MethodGet, path: "/config"})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if v, ok := body["turnstile_site_key"]; !ok || v != nil {
		t.Errorf("turnstile_site_key %v (want null: no Turnstile, D5)", v)
	}
	if body["min_password_length"] != float64(12) {
		t.Errorf("min_password_length %v", body["min_password_length"])
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestHostAndOriginChecks(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("host@example.com")

	t.Run("api host is 404", func(t *testing.T) {
		for _, r := range []req{
			{method: http.MethodGet, path: "/config", host: "api.compute.test"},
			{method: http.MethodGet, path: "/me", host: "api.compute.test", cookie: s.cookie},
			{method: http.MethodPost, path: "/login", host: "api.compute.test", body: map[string]string{}},
		} {
			wantErr(t, f.do(r), 404, "not_found", "not_found")
		}
	})
	t.Run("unknown path is a JSON 404", func(t *testing.T) {
		wantErr(t, f.do(req{method: http.MethodGet, path: "/keys"}), 404, "not_found", "not_found")
	})
	for _, o := range []struct {
		name   string
		origin *string
	}{
		{"cross-origin", strp("https://evil.example")},
		{"api host origin", strp("https://api.compute.test")},
		{"http downgrade", strp("http://app.compute.test")},
		{"missing", strp("")},
		{"null", strp("null")},
	} {
		t.Run(o.name+" POST refused", func(t *testing.T) {
			r := post("/login", map[string]string{"email": "host@example.com", "password": goodPass})
			r.origin = o.origin
			wantErr(t, f.do(r), 403, "permission_error", "csrf_failed")
			r = req{method: http.MethodPost, path: "/logout", cookie: s.cookie, csrf: s.csrf, origin: o.origin}
			wantErr(t, f.do(r), 403, "permission_error", "csrf_failed")
		})
	}
	// The refused logout above must not have ended the session.
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}); rec.Code != 200 {
		t.Fatalf("session gone after refused cross-origin logout: %d", rec.Code)
	}
}

func TestCSRFTokenRequiredOnSessionWrites(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("csrf@example.com")
	other := f.activeUser("csrf2@example.com")
	for _, c := range []struct{ name, token string }{{"missing", ""}, {"wrong", "nope"}, {"another session's", other.csrf}} {
		for _, path := range []string{"/logout", "/password", "/reauth"} {
			rec := f.do(req{method: http.MethodPost, path: path, cookie: s.cookie, csrf: c.token, body: map[string]string{}})
			if rec.Code != 403 || decodeErr(t, rec).Error.Code != "csrf_failed" {
				t.Errorf("%s token on %s: %d %s", c.name, path, rec.Code, rec.Body)
			}
		}
	}
	// GET needs no token.
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}); rec.Code != 200 {
		t.Fatalf("/me %d", rec.Code)
	}
	if rec := f.do(req{method: http.MethodPost, path: "/logout", cookie: s.cookie, csrf: s.csrf}); rec.Code != 204 {
		t.Fatalf("logout with token %d %s", rec.Code, rec.Body)
	}
}

func TestSessionRoutesWithoutASession(t *testing.T) {
	f := newFixture(t)
	for _, r := range []req{
		{method: http.MethodGet, path: "/me"},
		{method: http.MethodGet, path: "/me", cookie: "forged"},
		{method: http.MethodPost, path: "/logout", cookie: "forged", csrf: "x"},
		{method: http.MethodPost, path: "/password", body: map[string]string{}},
		{method: http.MethodPost, path: "/reauth", body: map[string]string{}},
	} {
		rec := f.do(r)
		wantErr(t, rec, 401, "authentication_error", "session_required")
		if c := sessionCookie(t, rec); (r.cookie != "") != (c != nil && c.MaxAge < 0) {
			t.Errorf("%s %s cookie %q: clearing cookie %v", r.method, r.path, r.cookie, c)
		}
	}
}

func TestSignupErrors(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		body   any
		status int
		code   string
		param  string
	}{
		{"malformed", "{not json", 400, "malformed_body", ""},
		{"honeypot", map[string]string{"email": "a@example.com", "password": goodPass, "password_confirm": goodPass, "website": "http://spam"}, 400, "bot_check_failed", ""},
		{"invalid email", map[string]string{"email": "not-an-address", "password": goodPass, "password_confirm": goodPass}, 400, "invalid_email", "email"},
		{"mismatch", map[string]string{"email": "a@example.com", "password": goodPass, "password_confirm": goodPass + "x"}, 400, "password_mismatch", "password_confirm"},
		{"short", map[string]string{"email": "a@example.com", "password": "short", "password_confirm": "short"}, 400, "weak_password", "password"},
		{"is the local part", map[string]string{"email": "averylongname@example.com", "password": "averylongname", "password_confirm": "averylongname"}, 400, "weak_password", "password"},
		{"few distinct", map[string]string{"email": "a@example.com", "password": "abababababab", "password_confirm": "abababababab"}, 400, "weak_password", "password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := wantErr(t, f.do(post("/signup", c.body)), c.status, "invalid_request", c.code)
			if e.Error.Param != c.param {
				t.Errorf("param %q, want %q", e.Error.Param, c.param)
			}
		})
	}
	f.h.Wait()
	if n := len(f.mail.all()); n != 0 {
		t.Errorf("%d emails sent for refused signups", n)
	}
	// Refusals before the throttle do not use up the hour.
	for i := range 5 {
		if rec := f.do(post("/signup", signupBody("ok"+strconv.Itoa(i)+"@example.com"))); rec.Code != 202 {
			t.Fatalf("signup %d after refusals: %d %s", i, rec.Code, rec.Body)
		}
	}
}

func TestSignupNewAndRegisteredAddressLookTheSame(t *testing.T) {
	f := newFixture(t)
	first := f.do(post("/signup", signupBody("Taken@Example.com")))
	if first.Code != 202 {
		t.Fatalf("%d %s", first.Code, first.Body)
	}
	again := f.do(post("/signup", signupBody("taken@example.com")))
	fresh := f.do(post("/signup", signupBody("fresh@example.com")))
	for _, rec := range []*httptest.ResponseRecorder{again, fresh} {
		if rec.Code != first.Code || rec.Body.String() != first.Body.String() {
			t.Errorf("response %d %s differs from %d %s", rec.Code, rec.Body, first.Code, first.Body)
		}
		if sessionCookie(t, rec) != nil {
			t.Error("signup set a cookie")
		}
	}
	if !strings.Contains(first.Body.String(), `"verification_sent"`) {
		t.Errorf("body %s", first.Body)
	}
	f.h.Wait()
	var kinds []string
	for _, m := range f.mail.to("taken@example.com") {
		kinds = append(kinds, m.kind)
	}
	if !slices.Equal(kinds, []string{"verification-code", "signup-attempt"}) {
		t.Errorf("mail to the registered address: %v", kinds)
	}
	var n int
	_ = f.db.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email = 'taken@example.com'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d users for the address", n)
	}
	acct := f.accountOf("taken@example.com")
	if got := f.auditActions(acct); !slices.Equal(got, []string{"signup", "signup"}) {
		t.Errorf("audit %v", got)
	}
}

func TestSignupRolledBackWhenTheCodeCannotBeSent(t *testing.T) {
	f := newFixture(t)
	f.mail.fail.Store(true)
	rec := f.do(post("/signup", signupBody("bounce@example.com")))
	wantErr(t, rec, 503, "email_unavailable", "email_unavailable")
	if _, err := f.acc.UserByEmail(context.Background(), "bounce@example.com"); err != accounts.ErrNotFound {
		t.Fatalf("user kept after rollback: %v", err)
	}
	var accts int
	_ = f.db.QueryRow(context.Background(), `SELECT count(*) FROM accounts`).Scan(&accts)
	if accts != 0 {
		t.Errorf("%d accounts kept", accts)
	}
	// A registered address with SES down still answers 202: a 503 there
	// would tell the two apart.
	f.mail.fail.Store(false)
	f.activeUser("exists@example.com")
	f.mail.fail.Store(true)
	if rec := f.do(post("/signup", signupBody("exists@example.com"))); rec.Code != 202 {
		t.Errorf("registered address with SES down: %d", rec.Code)
	}
	f.mail.fail.Store(false)
	if rec := f.do(post("/signup", signupBody("bounce@example.com"))); rec.Code != 202 {
		t.Fatalf("address not freed: %d %s", rec.Code, rec.Body)
	}
}

func TestSignupThrottlePerIP(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		if rec := f.do(post("/signup", signupBody("s"+strconv.Itoa(i)+"@example.com"))); rec.Code != 202 {
			t.Fatalf("signup %d: %d", i, rec.Code)
		}
	}
	r := post("/signup", signupBody("s5@example.com"))
	r.xff = "9.9.9.9, 203.0.113.9" // a forged first entry changes nothing
	rec := f.do(r)
	wantErr(t, rec, 429, "rate_limited", "rate_limited")
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 3600 {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	r.xff = "203.0.113.10"
	if rec := f.do(r); rec.Code != 202 {
		t.Errorf("another client refused: %d", rec.Code)
	}
	f.clock.advance(time.Hour)
	if rec := f.do(post("/signup", signupBody("s6@example.com"))); rec.Code != 202 {
		t.Errorf("after the hour: %d", rec.Code)
	}
}

func TestVerify(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(post("/signup", signupBody("v@example.com"))); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	code := f.latestCode("v@example.com")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for _, body := range []any{
		map[string]string{"email": "v@example.com", "code": wrong},
		map[string]string{"email": "nobody@example.com", "code": code},
		map[string]string{"email": "v@example.com", "code": ""},
	} {
		e := wantErr(t, f.do(post("/verify", body)), 400, "invalid_request", "invalid_code")
		if e.Error.Param != "code" {
			t.Errorf("param %q", e.Error.Param)
		}
	}
	wantErr(t, f.do(post("/verify", "[")), 400, "invalid_request", "malformed_body")

	// A pending user cannot log in yet, and only after the right password.
	wantErr(t, f.login("v@example.com", goodPass), 403, "authentication_error", "email_unverified")
	wantErr(t, f.login("v@example.com", "wrong password here"), 401, "authentication_error", "invalid_credentials")

	rec := f.do(post("/verify", map[string]string{"email": "V@example.com ", "code": code[:3] + " " + code[3:]}))
	if rec.Code != 200 {
		t.Fatalf("verify %d %s", rec.Code, rec.Body)
	}
	c := sessionCookie(t, rec)
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie %+v", c)
	}
	if c.MaxAge != int(accounts.SessionAbsolute/time.Second) {
		t.Errorf("Max-Age %d", c.MaxAge)
	}
	var body struct {
		User struct {
			Email     string `json:"email"`
			CreatedAt int64  `json:"created_at"`
		} `json:"user"`
		Account struct {
			ID             int64  `json:"id"`
			Status         string `json:"status"`
			ComputeEnabled *bool  `json:"compute_enabled"`
		} `json:"account"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.User.Email != "v@example.com" || body.User.CreatedAt != f.clock.now().Unix() || body.Account.ID == 0 ||
		body.Account.Status != "active" || body.Account.ComputeEnabled == nil || *body.Account.ComputeEnabled || body.CSRFToken == "" {
		t.Errorf("body %s", rec.Body)
	}
	// The code is spent.
	wantErr(t, f.do(post("/verify", map[string]string{"email": "v@example.com", "code": code})), 400, "invalid_request", "invalid_code")
	if got := f.auditActions(body.Account.ID); !slices.Equal(got, []string{"signup", "login_failed", "login_failed", "verify"}) {
		t.Errorf("audit %v", got)
	}
}

func TestVerifyThrottleCountsWrongCodesPerIP(t *testing.T) {
	f := newFixture(t)
	for i := range 20 {
		rec := f.do(post("/verify", map[string]string{"email": "x" + strconv.Itoa(i) + "@example.com", "code": "123456"}))
		if rec.Code != 400 {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	r := post("/verify", map[string]string{"email": "x@example.com", "code": "123456"})
	r.xff = "6.6.6.6, 203.0.113.9"
	wantErr(t, f.do(r), 429, "rate_limited", "rate_limited")
	r.xff = "203.0.113.77"
	if rec := f.do(r); rec.Code != 400 {
		t.Errorf("other client: %d", rec.Code)
	}
}

func TestResend(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(post("/signup", signupBody("r@example.com"))); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	first := f.latestCode("r@example.com")
	f.activeUser("active@example.com")
	f.clock.advance(time.Minute)

	wantErr(t, f.do(post("/verify/resend", map[string]string{"email": "nope"})), 400, "invalid_request", "invalid_email")
	wantErr(t, f.do(post("/verify/resend", map[string]string{"email": "r@example.com", "website": "x"})), 400, "invalid_request", "bot_check_failed")

	var bodies []string
	for _, addr := range []string{"r@example.com", "active@example.com", "unknown@example.com"} {
		rec := f.do(post("/verify/resend", map[string]string{"email": addr}))
		if rec.Code != 202 {
			t.Fatalf("%s: %d %s", addr, rec.Code, rec.Body)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("bodies differ: %v", bodies)
	}
	f.h.Wait()
	if n := len(f.mail.to("active@example.com")); n != 1 {
		t.Errorf("active user got %d messages (want only the original code)", n)
	}
	if n := len(f.mail.to("unknown@example.com")); n != 0 {
		t.Errorf("unknown address got mail")
	}
	second := f.latestCode("r@example.com")
	if second == first {
		t.Skip("new code equals the old by chance (1 in a million)")
	}
	// The old code is superseded.
	wantErr(t, f.do(post("/verify", map[string]string{"email": "r@example.com", "code": first})), 400, "invalid_request", "invalid_code")

	// 1 a minute per address, whatever the IP.
	r := post("/verify/resend", map[string]string{"email": "r@example.com"})
	r.xff = "203.0.113.200"
	rec := f.do(r)
	wantErr(t, rec, 429, "rate_limited", "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// The unknown address is throttled the same way: no oracle.
	wantErr(t, f.do(post("/verify/resend", map[string]string{"email": "unknown@example.com"})), 429, "rate_limited", "rate_limited")

	// 5 an hour.
	for range 4 {
		f.clock.advance(time.Minute)
		if rec := f.do(post("/verify/resend", map[string]string{"email": "r@example.com"})); rec.Code != 202 {
			t.Fatalf("resend: %d", rec.Code)
		}
	}
	f.clock.advance(time.Minute)
	wantErr(t, f.do(post("/verify/resend", map[string]string{"email": "r@example.com"})), 429, "rate_limited", "rate_limited")
	rec = f.do(post("/verify", map[string]string{"email": "r@example.com", "code": f.latestCode("r@example.com")}))
	if rec.Code != 200 {
		t.Fatalf("latest code after resends: %d %s", rec.Code, rec.Body)
	}
}

func TestLoginErrorsAndSuccess(t *testing.T) {
	f := newFixture(t)
	f.activeUser("l@example.com")
	f.activeUser("disabled@example.com")
	if _, err := f.db.Exec(context.Background(), `UPDATE users SET status = 'disabled' WHERE email = 'disabled@example.com'`); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		body      any
		status    int
		typ, code string
	}{
		{"malformed", "nope", 400, "invalid_request", "malformed_body"},
		{"honeypot", map[string]string{"email": "l@example.com", "password": goodPass, "website": "x"}, 400, "invalid_request", "bot_check_failed"},
		{"invalid email", map[string]string{"email": "l", "password": goodPass}, 400, "invalid_request", "invalid_email"},
		{"wrong password", map[string]string{"email": "l@example.com", "password": "not the password"}, 401, "authentication_error", "invalid_credentials"},
		{"unknown email", map[string]string{"email": "ghost@example.com", "password": goodPass}, 401, "authentication_error", "invalid_credentials"},
		{"disabled, right password", map[string]string{"email": "disabled@example.com", "password": goodPass}, 403, "authentication_error", "account_disabled"},
		{"disabled, wrong password", map[string]string{"email": "disabled@example.com", "password": "not the password"}, 401, "authentication_error", "invalid_credentials"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(post("/login", c.body))
			wantErr(t, rec, c.status, c.typ, c.code)
			if sessionCookie(t, rec) != nil {
				t.Error("cookie set on a refused login")
			}
		})
	}
	rec := f.login(" L@Example.com", goodPass)
	if rec.Code != 200 {
		t.Fatalf("login %d %s", rec.Code, rec.Body)
	}
	s := sessionFrom(t, rec)
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}); rec.Code != 200 || !strings.Contains(rec.Body.String(), s.csrf) {
		t.Fatalf("/me %d %s", rec.Code, rec.Body)
	}
	acct := f.accountOf("l@example.com")
	if got := f.auditActions(acct); !slices.Equal(got, []string{"signup", "verify", "login_failed", "login"}) {
		t.Errorf("audit %v", got)
	}
}

func TestLoginRotatesTheSession(t *testing.T) {
	f := newFixture(t)
	old := f.activeUser("rot@example.com")
	r := post("/login", map[string]string{"email": "rot@example.com", "password": goodPass})
	r.cookie = old.cookie
	rec := f.do(r)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if s := sessionFrom(t, rec); s.cookie == old.cookie || s.csrf == old.csrf {
		t.Error("session not rotated")
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: old.cookie}), 401, "authentication_error", "session_required")
}

// TestLoginDoesNotRevealWhetherTheEmailExists: same status, body and number
// of argon2id evaluations for an unknown address and a wrong password.
func TestLoginDoesNotRevealWhetherTheEmailExists(t *testing.T) {
	f := newFixture(t)
	f.activeUser("known@example.com")
	var bodies []string
	var costs []int64
	for _, email := range []string{"known@example.com", "unknown@example.com"} {
		before := f.hasher.verifies.Load()
		rec := f.login(email, "definitely wrong pw")
		costs = append(costs, f.hasher.verifies.Load()-before)
		if rec.Code != 401 {
			t.Fatalf("%s: %d", email, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Errorf("bodies differ:\n%s\n%s", bodies[0], bodies[1])
	}
	if costs[0] != 1 || costs[1] != 1 {
		t.Errorf("argon2id evaluations known=%d unknown=%d, want 1 each", costs[0], costs[1])
	}
}

func TestLoginThrottlePerIPCountsEveryAttempt(t *testing.T) {
	f := newFixture(t)
	f.activeUser("ip@example.com")
	for i := range 20 {
		r := post("/login", map[string]string{"email": "ip@example.com", "password": goodPass})
		// Varying the first entry is what a client controls; the ALB's
		// last entry stays the same.
		r.xff = "10.0." + strconv.Itoa(i) + ".1, 203.0.113.9"
		if rec := f.do(r); rec.Code != 200 {
			t.Fatalf("login %d: %d", i, rec.Code)
		}
	}
	before := f.hasher.verifies.Load()
	rec := f.login("ip@example.com", goodPass)
	wantErr(t, rec, 429, "rate_limited", "rate_limited")
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 600 {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if f.hasher.verifies.Load() != before {
		t.Error("a throttled login still hashed")
	}
	r := post("/login", map[string]string{"email": "ip@example.com", "password": goodPass})
	r.xff = "203.0.113.10"
	if rec := f.do(r); rec.Code != 200 {
		t.Errorf("another client: %d", rec.Code)
	}
	f.clock.advance(10 * time.Minute)
	if rec := f.login("ip@example.com", goodPass); rec.Code != 200 {
		t.Errorf("after the window: %d", rec.Code)
	}
}

// TestLoginThrottlePerAddressCountsFailuresOnly: 50 failures from 50 IPs
// close the address for the window; successes never count; an unknown
// address is closed exactly like a known one.
func TestLoginThrottlePerAddressCountsFailuresOnly(t *testing.T) {
	f := newFixture(t)
	f.activeUser("acct@example.com")
	ip := 0
	attempt := func(email, pw string) *httptest.ResponseRecorder {
		ip++
		r := post("/login", map[string]string{"email": email, "password": pw})
		r.xff = "203.0.113.9, 10." + strconv.Itoa(ip/250) + "." + strconv.Itoa(ip%250) + ".1"
		return f.do(r)
	}
	for range 30 {
		if rec := attempt("acct@example.com", goodPass); rec.Code != 200 {
			t.Fatalf("success: %d", rec.Code)
		}
	}
	for _, email := range []string{"acct@example.com", "ghost@example.com"} {
		for i := range 50 {
			if rec := attempt(email, "wrong password!!"); rec.Code != 401 {
				t.Fatalf("%s failure %d: %d", email, i, rec.Code)
			}
		}
		wantErr(t, attempt(email, goodPass), 429, "rate_limited", "rate_limited")
	}
	f.clock.advance(10 * time.Minute)
	if rec := attempt("acct@example.com", goodPass); rec.Code != 200 {
		t.Errorf("after the window: %d", rec.Code)
	}
}

func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("exp@example.com")
	me := func() *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie})
	}

	// Used every 11 h, the session survives the 12 h idle limit...
	elapsed := time.Duration(0)
	for elapsed+11*time.Hour < accounts.SessionAbsolute {
		f.clock.advance(11 * time.Hour)
		elapsed += 11 * time.Hour
		if rec := me(); rec.Code != 200 {
			t.Fatalf("after %s of use: %d", elapsed, rec.Code)
		}
	}
	// ...until 7 days, which use does not extend.
	f.clock.advance(accounts.SessionAbsolute - elapsed)
	rec := me()
	wantErr(t, rec, 401, "authentication_error", "session_required")
	if c := sessionCookie(t, rec); c == nil || c.MaxAge >= 0 {
		t.Errorf("expired cookie not cleared: %+v", c)
	}

	idle := f.activeUser("idle@example.com")
	f.clock.advance(accounts.SessionIdle - time.Minute)
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: idle.cookie}); rec.Code != 200 {
		t.Fatalf("just inside idle: %d", rec.Code)
	}
	f.clock.advance(accounts.SessionIdle)
	wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: idle.cookie}), 401, "authentication_error", "session_required")
}

func TestForgotPasswordIsTheSameForEveryAddress(t *testing.T) {
	f := newFixture(t)
	f.activeUser("f@example.com")
	if rec := f.do(post("/signup", signupBody("pending@example.com"))); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	wantErr(t, f.do(post("/password/forgot", map[string]string{"email": "bad"})), 400, "invalid_request", "invalid_email")
	wantErr(t, f.do(post("/password/forgot", map[string]string{"email": "f@example.com", "website": "x"})), 400, "invalid_request", "bot_check_failed")
	var bodies []string
	for _, addr := range []string{"f@example.com", "pending@example.com", "nobody@example.com"} {
		rec := f.do(post("/password/forgot", map[string]string{"email": addr}))
		if rec.Code != 202 {
			t.Fatalf("%s: %d", addr, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("bodies differ: %v", bodies)
	}
	f.h.Wait()
	if len(f.mail.to("nobody@example.com")) != 0 {
		t.Error("mail to an unknown address")
	}
	f.latestResetToken("f@example.com")
	f.latestResetToken("pending@example.com")
	if got := f.auditActions(f.accountOf("f@example.com")); !slices.Contains(got, "reset_requested") {
		t.Errorf("audit %v", got)
	}
	// SES down: still 202.
	f.mail.fail.Store(true)
	if rec := f.do(post("/password/forgot", map[string]string{"email": "f@example.com"})); rec.Code != 202 {
		t.Errorf("SES down: %d", rec.Code)
	}
	f.h.Wait()
}

func TestForgotThrottles(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		if rec := f.do(post("/password/forgot", map[string]string{"email": "a" + strconv.Itoa(i) + "@example.com"})); rec.Code != 202 {
			t.Fatal(rec.Code)
		}
	}
	r := post("/password/forgot", map[string]string{"email": "z@example.com"})
	r.xff = "1.1.1.1, 203.0.113.9"
	wantErr(t, f.do(r), 429, "rate_limited", "rate_limited")
	// Per address, across IPs.
	for i := range 5 {
		r := post("/password/forgot", map[string]string{"email": "target@example.com"})
		r.xff = "198.51.100." + strconv.Itoa(i+1)
		if rec := f.do(r); rec.Code != 202 {
			t.Fatal(rec.Code)
		}
	}
	r = post("/password/forgot", map[string]string{"email": "target@example.com"})
	r.xff = "198.51.100.99"
	wantErr(t, f.do(r), 429, "rate_limited", "rate_limited")
	f.h.Wait()
}

func TestResetPassword(t *testing.T) {
	f := newFixture(t)
	a := f.activeUser("reset@example.com")
	b := sessionFrom(t, f.login("reset@example.com", goodPass))
	if rec := f.do(post("/password/forgot", map[string]string{"email": "reset@example.com"})); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	token := f.latestResetToken("reset@example.com")

	wantErr(t, f.do(post("/password/reset", "x")), 400, "invalid_request", "malformed_body")
	wantErr(t, f.do(post("/password/reset", map[string]string{"token": "bogus", "password": newPass, "password_confirm": newPass})), 400, "invalid_request", "invalid_token")
	wantErr(t, f.do(post("/password/reset", map[string]string{"token": token, "password": newPass, "password_confirm": "other"})), 400, "invalid_request", "password_mismatch")
	wantErr(t, f.do(post("/password/reset", map[string]string{"token": token, "password": "reset@example.com", "password_confirm": "reset@example.com"})), 400, "invalid_request", "weak_password")

	r := post("/password/reset", map[string]string{"token": token, "password": newPass, "password_confirm": newPass})
	r.cookie = a.cookie
	rec := f.do(r)
	if rec.Code != 200 {
		t.Fatalf("reset %d %s", rec.Code, rec.Body)
	}
	if c := sessionCookie(t, rec); c == nil || c.MaxAge >= 0 {
		t.Error("reset did not clear the cookie")
	}
	for _, s := range []session{a, b} {
		wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}), 401, "authentication_error", "session_required")
	}
	wantErr(t, f.do(post("/password/reset", map[string]string{"token": token, "password": newPass, "password_confirm": newPass})), 400, "invalid_request", "invalid_token")
	wantErr(t, f.login("reset@example.com", goodPass), 401, "authentication_error", "invalid_credentials")
	if rec := f.login("reset@example.com", newPass); rec.Code != 200 {
		t.Fatalf("login with new password: %d", rec.Code)
	}
	f.h.Wait()
	msgs := f.mail.to("reset@example.com")
	if msgs[len(msgs)-1].kind != "password-changed" {
		t.Errorf("last message %s", msgs[len(msgs)-1].kind)
	}
	if got := f.auditActions(f.accountOf("reset@example.com")); !slices.Contains(got, "password_reset") {
		t.Errorf("audit %v", got)
	}
}

func TestResetVerifiesAPendingUser(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(post("/signup", signupBody("p@example.com"))); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	if rec := f.do(post("/password/forgot", map[string]string{"email": "p@example.com"})); rec.Code != 202 {
		t.Fatalf("forgot %d %s", rec.Code, rec.Body)
	}
	token := f.latestResetToken("p@example.com")
	if rec := f.do(post("/password/reset", map[string]string{"token": token, "password": newPass, "password_confirm": newPass})); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := f.login("p@example.com", newPass); rec.Code != 200 {
		t.Fatalf("login after reset of a pending user: %d %s", rec.Code, rec.Body)
	}
}

func TestChangePassword(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("c@example.com")
	other := sessionFrom(t, f.login("c@example.com", goodPass))
	change := func(body any) *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodPost, path: "/password", cookie: s.cookie, csrf: s.csrf, body: body})
	}
	wantErr(t, change("{"), 400, "invalid_request", "malformed_body")
	wantErr(t, change(map[string]string{"current_password": goodPass, "password": newPass, "password_confirm": "x"}), 400, "invalid_request", "password_mismatch")
	wantErr(t, change(map[string]string{"current_password": goodPass, "password": "short", "password_confirm": "short"}), 400, "invalid_request", "weak_password")
	e := wantErr(t, change(map[string]string{"current_password": "not my password", "password": newPass, "password_confirm": newPass}), 401, "authentication_error", "invalid_credentials")
	if e.Error.Param != "current_password" {
		t.Errorf("param %q", e.Error.Param)
	}
	// A wrong current password does not end the session.
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}); rec.Code != 200 {
		t.Fatal("session ended by a wrong current password")
	}

	f.clock.advance(time.Hour) // older than the reauth window: the current password is the reauth
	if rec := change(map[string]string{"current_password": goodPass, "password": newPass, "password_confirm": newPass}); rec.Code != 204 {
		t.Fatalf("change %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}); rec.Code != 200 {
		t.Error("the changing session was ended")
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: other.cookie}), 401, "authentication_error", "session_required")
	sess, _, err := f.acc.LookupSession(context.Background(), s.cookie)
	if err != nil || !sess.ReauthFresh(f.clock.now()) {
		t.Errorf("change did not count as reauth: %v", err)
	}
	f.h.Wait()
	msgs := f.mail.to("c@example.com")
	if msgs[len(msgs)-1].kind != "password-changed" {
		t.Errorf("last message %s", msgs[len(msgs)-1].kind)
	}
	got := f.auditActions(f.accountOf("c@example.com"))
	if !slices.Contains(got, "password_changed") || !slices.Contains(got, "login_failed") {
		t.Errorf("audit %v", got)
	}
}

func TestReauth(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("re@example.com")
	f.clock.advance(accounts.ReauthWindow + time.Minute)
	if sess, _, _ := f.acc.LookupSession(context.Background(), s.cookie); sess.ReauthFresh(f.clock.now()) {
		t.Fatal("fresh before reauth")
	}
	reauth := func(pw string) *httptest.ResponseRecorder {
		return f.do(req{method: http.MethodPost, path: "/reauth", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"password": pw}})
	}
	e := wantErr(t, reauth("wrong password!!"), 401, "authentication_error", "invalid_credentials")
	if e.Error.Param != "password" {
		t.Errorf("param %q", e.Error.Param)
	}
	if rec := reauth(goodPass); rec.Code != 204 {
		t.Fatalf("reauth %d %s", rec.Code, rec.Body)
	}
	if sess, _, _ := f.acc.LookupSession(context.Background(), s.cookie); !sess.ReauthFresh(f.clock.now()) {
		t.Error("reauth_at not set")
	}
}

func TestWrongPasswordInASessionIsThrottledLikeLogin(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("guess@example.com")
	for i := range 50 {
		rec := f.do(req{method: http.MethodPost, path: "/reauth", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"password": "guess " + strconv.Itoa(i) + "abcdefgh"}})
		if rec.Code != 401 {
			t.Fatalf("guess %d: %d", i, rec.Code)
		}
	}
	rec := f.do(req{method: http.MethodPost, path: "/reauth", cookie: s.cookie, csrf: s.csrf, body: map[string]string{"password": goodPass}})
	wantErr(t, rec, 429, "rate_limited", "rate_limited")
}

func TestLogout(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("out@example.com")
	rec := f.do(req{method: http.MethodPost, path: "/logout", cookie: s.cookie, csrf: s.csrf})
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if c := sessionCookie(t, rec); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("cookie not cleared: %+v", c)
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}), 401, "authentication_error", "session_required")
	if got := f.auditActions(f.accountOf("out@example.com")); got[len(got)-1] != "logout" {
		t.Errorf("audit %v", got)
	}
}

// TestFullFlow is the path a new customer takes, with the mail captured:
// signup, the emailed code, verify, log in, change password, log out, and
// log in again with the new password only.
func TestFullFlow(t *testing.T) {
	f := newFixture(t)
	const email = "flow@example.com"
	if rec := f.do(post("/signup", signupBody(email))); rec.Code != 202 {
		t.Fatalf("signup %d", rec.Code)
	}
	verify := f.do(post("/verify", map[string]string{"email": email, "code": f.latestCode(email)}))
	if verify.Code != 200 {
		t.Fatalf("verify %d %s", verify.Code, verify.Body)
	}
	vs := sessionFrom(t, verify)
	if rec := f.do(req{method: http.MethodPost, path: "/logout", cookie: vs.cookie, csrf: vs.csrf}); rec.Code != 204 {
		t.Fatalf("logout after verify %d", rec.Code)
	}

	s := sessionFrom(t, f.login(email, goodPass))
	me := f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie})
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"email":"flow@example.com"`) {
		t.Fatalf("/me %d %s", me.Code, me.Body)
	}
	if rec := f.do(req{method: http.MethodPost, path: "/password", cookie: s.cookie, csrf: s.csrf,
		body: map[string]string{"current_password": goodPass, "password": newPass, "password_confirm": newPass}}); rec.Code != 204 {
		t.Fatalf("change %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(req{method: http.MethodPost, path: "/logout", cookie: s.cookie, csrf: s.csrf}); rec.Code != 204 {
		t.Fatalf("logout %d", rec.Code)
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/me", cookie: s.cookie}), 401, "authentication_error", "session_required")
	wantErr(t, f.login(email, goodPass), 401, "authentication_error", "invalid_credentials")
	if rec := f.login(email, newPass); rec.Code != 200 {
		t.Fatalf("login with new password %d", rec.Code)
	}

	f.h.Wait()
	var kinds []string
	for _, m := range f.mail.to(email) {
		kinds = append(kinds, m.kind)
	}
	if !slices.Equal(kinds, []string{"verification-code", "password-changed"}) {
		t.Errorf("mail %v", kinds)
	}
	want := []string{"signup", "verify", "logout", "login", "password_changed", "logout", "login_failed", "login"}
	if got := f.auditActions(f.accountOf(email)); !slices.Equal(got, want) {
		t.Errorf("audit\n got %v\nwant %v", got, want)
	}
	var ip, ua string
	_ = f.db.QueryRow(context.Background(), `SELECT ip, user_agent FROM audit_events WHERE action = 'login' LIMIT 1`).Scan(&ip, &ua)
	if ip != "203.0.113.9" || ua != "dashboard-test" {
		t.Errorf("audit client %q %q", ip, ua)
	}
}
