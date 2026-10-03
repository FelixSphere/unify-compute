package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

const (
	testOrigin = "https://app.compute.test"
	testHost   = "app.compute.test"
	goodPass   = "correct horse battery"
	newPass    = "another fine passphrase"
)

var cheapParams = accounts.Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingHasher counts argon2id evaluations, real or dummy, so a test can
// prove an unknown address costs what a known one does.
type countingHasher struct {
	*accounts.Hasher
	verifies atomic.Int64
}

func (h *countingHasher) Verify(ctx context.Context, pw, enc string) (bool, bool, error) {
	h.verifies.Add(1)
	return h.Hasher.Verify(ctx, pw, enc)
}

func (h *countingHasher) VerifyDummy(ctx context.Context, pw string) error {
	h.verifies.Add(1)
	return h.Hasher.VerifyDummy(ctx, pw)
}

type sent struct {
	kind, to string
	text     string
}

// capture is the mailer transport under test: it keeps every message and
// can be told to refuse, as SES would.
type capture struct {
	mu   sync.Mutex
	msgs []sent
	fail atomic.Bool
}

func (c *capture) Deliver(_ context.Context, kind, to string, raw []byte) error {
	if c.fail.Load() {
		return errors.New("ses: MessageRejected")
	}
	text, err := plainText(raw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, sent{kind, to, text})
	c.mu.Unlock()
	return nil
}

func (c *capture) Check(context.Context) error { return nil }

func (c *capture) all() []sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sent(nil), c.msgs...)
}

func (c *capture) to(addr string) []sent {
	var out []sent
	for _, m := range c.all() {
		if m.to == addr {
			out = append(out, m)
		}
	}
	return out
}

func plainText(raw []byte) (string, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		return "", err
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain") {
			// NextPart has already undone the quoted-printable encoding.
			b, err := io.ReadAll(p)
			return string(b), err
		}
	}
}

var (
	codeRe  = regexp.MustCompile(`Your code: (\d{6})`)
	tokenRe = regexp.MustCompile(`#token=(\S+)`)
)

type fixture struct {
	t      *testing.T
	h      *Handler
	acc    *accounts.Service
	db     *pgxpool.Pool
	clock  *clock
	hasher *countingHasher
	mail   *capture
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := storetest.New(t)
	c := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	hs := &countingHasher{Hasher: accounts.NewHasher(cheapParams)}
	acc := accounts.New(st.Pool(), accounts.Config{Hasher: hs, Now: c.now})
	cap := &capture{}
	m := &mailer.Mailer{From: mail.Address{Name: "Unify Compute", Address: "no-reply@compute.unifyai.us"},
		AppOrigin: testOrigin, Transport: cap, Now: c.now}
	h, err := New(Config{Accounts: acc, Mailer: m, AppOrigin: testOrigin, Log: slog.New(slog.NewTextHandler(testLogWriter{t}, nil)), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, h: h, acc: acc, db: st.Pool(), clock: c, hasher: hs, mail: cap}
}

// req describes one request. Zero values are the well-behaved browser:
// the app host, a same-origin Origin on non-GETs, and the client 203.0.113.9
// as the ALB's last X-Forwarded-For entry.
type req struct {
	method, path string
	body         any
	cookie       string
	csrf         string
	host         string
	origin       *string
	xff          string
}

func strp(s string) *string { return &s }

func (f *fixture) do(r req) *httptest.ResponseRecorder {
	f.t.Helper()
	var body io.Reader = http.NoBody
	switch b := r.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	hr := httptest.NewRequest(r.method, Prefix+r.path, body)
	hr.Host = testHost
	if r.host != "" {
		hr.Host = r.host
	}
	if r.origin != nil {
		if *r.origin != "" {
			hr.Header.Set("Origin", *r.origin)
		}
	} else if r.method != http.MethodGet {
		hr.Header.Set("Origin", testOrigin)
	}
	xff := r.xff
	if xff == "" {
		xff = "203.0.113.9"
	}
	hr.Header.Set("X-Forwarded-For", xff)
	hr.Header.Set("User-Agent", "dashboard-test")
	if r.cookie != "" {
		hr.AddCookie(&http.Cookie{Name: CookieName, Value: r.cookie})
	}
	if r.csrf != "" {
		hr.Header.Set(CSRFHeader, r.csrf)
	}
	hr.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, hr)
	return rec
}

func post(path string, body any) req { return req{method: http.MethodPost, path: path, body: body} }

type errBody struct {
	Error struct {
		Type, Code, Message, Param string
	}
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) errBody {
	t.Helper()
	var e errBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("not an error envelope: %s", rec.Body)
	}
	return e
}

func wantErr(t *testing.T, rec *httptest.ResponseRecorder, status int, typ, code string) errBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	e := decodeErr(t, rec)
	if e.Error.Type != typ || e.Error.Code != code || e.Error.Message == "" {
		t.Fatalf("error %+v, want %s/%s", e.Error, typ, code)
	}
	return e
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

func signupBody(email string) map[string]string {
	return map[string]string{"email": email, "password": goodPass, "password_confirm": goodPass}
}

// latestCode waits for queued mail and returns the newest code sent to addr.
func (f *fixture) latestCode(addr string) string {
	f.t.Helper()
	f.h.Wait()
	msgs := f.mail.to(addr)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].kind == "verification-code" {
			if m := codeRe.FindStringSubmatch(msgs[i].text); m != nil {
				return m[1]
			}
		}
	}
	f.t.Fatalf("no verification code sent to %s (sent: %+v)", addr, msgs)
	return ""
}

func (f *fixture) latestResetToken(addr string) string {
	f.t.Helper()
	f.h.Wait()
	msgs := f.mail.to(addr)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].kind == "password-reset" {
			if m := tokenRe.FindStringSubmatch(msgs[i].text); m != nil {
				tok, err := url.QueryUnescape(m[1])
				if err != nil {
					f.t.Fatal(err)
				}
				return tok
			}
		}
	}
	f.t.Fatalf("no reset link sent to %s: %+v", addr, msgs)
	return ""
}

type session struct {
	cookie, csrf string
}

// activeUser signs up, verifies, and returns the session verify started.
func (f *fixture) activeUser(email string) session {
	f.t.Helper()
	if rec := f.do(post("/signup", signupBody(email))); rec.Code != http.StatusAccepted {
		f.t.Fatalf("signup %d: %s", rec.Code, rec.Body)
	}
	rec := f.do(post("/verify", map[string]string{"email": email, "code": f.latestCode(email)}))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("verify %d: %s", rec.Code, rec.Body)
	}
	return sessionFrom(f.t, rec)
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) session {
	t.Helper()
	c := sessionCookie(t, rec)
	if c == nil || c.Value == "" {
		t.Fatalf("no session cookie: %v", rec.Result().Header)
	}
	var body struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.CSRFToken == "" {
		t.Fatalf("no csrf_token: %s", rec.Body)
	}
	return session{cookie: c.Value, csrf: body.CSRFToken}
}

func (f *fixture) login(email, password string) *httptest.ResponseRecorder {
	return f.do(post("/login", map[string]string{"email": email, "password": password}))
}

// auditActions lists an account's audit actions in write order.
func (f *fixture) auditActions(accountID int64) []string {
	f.t.Helper()
	rows, err := f.db.Query(context.Background(), `SELECT action FROM audit_events WHERE account_id = $1 ORDER BY seq`, accountID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func (f *fixture) accountOf(email string) int64 {
	f.t.Helper()
	u, err := f.acc.UserByEmail(context.Background(), email)
	if err != nil {
		f.t.Fatalf("user %s: %v", email, err)
	}
	return u.AccountID
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
