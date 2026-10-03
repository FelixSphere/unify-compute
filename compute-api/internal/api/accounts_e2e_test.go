package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/dashboard"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ratelimit"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

// The ACCOUNTS_BACKEND=local end-to-end test (task A5): main's local wiring
// (accounts as the key verifier, the local biller as biller and submit
// gate, the dashboard mounted beside the job API, the admin CLI on the same
// database) against Postgres, the fake SkyPilot and the fake S3, on one fake
// clock. No console exists in this harness at all.
//
// Job API responses go through e2e.call and so are held against
// openapi.json like TestEndToEnd's, and their error codes are recorded in
// e2eSeen. This file sorts before e2e_test.go, so in a package run this test
// runs first and TestEndToEnd's "every listed code is produced" check sees
// the codes only this backend emits (scope_denied, compute_not_enabled).

const (
	acctOrigin = "https://app.compute.test"
	acctHost   = "app.compute.test"
	acctPass   = "correct horse battery"
)

var codeRe = regexp.MustCompile(`Your code: (\d{6})`)

type sentMail struct{ kind, to, text string }

// mailCapture is the mailer transport: it keeps every message's plain-text
// part.
type mailCapture struct {
	mu   sync.Mutex
	msgs []sentMail
}

func (c *mailCapture) Deliver(_ context.Context, kind, to string, raw []byte) error {
	text, err := plainPart(raw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.msgs = append(c.msgs, sentMail{kind, to, text})
	c.mu.Unlock()
	return nil
}

func (c *mailCapture) Check(context.Context) error { return nil }

func (c *mailCapture) of(kind, to string) []sentMail {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []sentMail
	for _, m := range c.msgs {
		if m.kind == kind && m.to == to {
			out = append(out, m)
		}
	}
	return out
}

func (c *mailCapture) all() []sentMail {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentMail(nil), c.msgs...)
}

func plainPart(raw []byte) (string, error) {
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
			b, err := io.ReadAll(p)
			return string(b), err
		}
	}
}

type acctE2E struct {
	*e2e
	acc  *accounts.Service
	bill *billing.Local
	dash *dashboard.Handler
	mail *mailCapture
	m    *mailer.Mailer
}

func newAccountsE2E(t *testing.T) *acctE2E {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	clock := &e2eClock{t: time.Now().Add(time.Minute).Truncate(time.Second)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cat, err := sku.Load(filepath.Join("..", "..", "config", "skus.json"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.Load(filepath.Join("..", "..", "config", "templates.json"), cat, MaxRuntimeSeconds)
	if err != nil {
		t.Fatal(err)
	}
	quotas, err := quota.Load(filepath.Join("..", "..", "config", "quotas.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := upstream.Parse("aws")
	if err != nil {
		t.Fatal(err)
	}
	sky := skypilottest.New()
	sky.Token = "sky_e2e_token"
	t.Cleanup(sky.Close)
	sky.SetClock(clock.Now)

	h := &e2e{t: t, ctx: ctx, clock: clock, st: st, sky: sky}
	h.srv = New("e2e-local", cat, reg, log)
	h.srv.Templates = tpl
	h.http = httptest.NewServer(h.srv)
	t.Cleanup(h.http.Close)

	h.s3 = awsv4test.New(t, "unify-compute-e2e-artifacts")
	art, err := artifacts.New(artifacts.Config{
		Bucket: h.s3.Bucket, WriterRoleARN: "arn:aws:iam::123456789012:role/unify-compute-e2e-artifacts-writer",
		PublicURL: h.http.URL, TokenSecret: []byte(strings.Repeat("t", 32)),
		Creds: awsv4.Static(h.s3.Instance), Client: h.s3.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Artifacts = art

	bill := billing.New(st.Pool(), clock.Now)
	h.svc = jobs.New(jobs.Config{
		Store: st, Biller: bill, Gate: bill, Catalogue: cat, Log: log, Now: clock.Now, Outputs: art,
		SkyPilot: &backend.Client{BaseURL: sky.URL, Token: sky.Token, UserHash: "unifycmp"},
		Quota:    &quota.Enforcer{Config: quotas, Catalogue: cat, Usage: st},
	})
	acc := accounts.New(st.Pool(), accounts.Config{Hasher: accounts.NewHasher(accounts.Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}), Now: clock.Now})
	h.srv.JobService, h.srv.Now = h.svc, clock.Now
	h.srv.Keys, h.srv.Owner, h.srv.Events, h.srv.Jobs = acc, KeyOwner(acc), st, st

	capture := &mailCapture{}
	m := &mailer.Mailer{From: mail.Address{Name: "Unify Compute", Address: "no-reply@compute.unifyai.us"},
		AppOrigin: acctOrigin, Transport: capture, Now: clock.Now}
	dash, err := dashboard.New(dashboard.Config{Accounts: acc, Mailer: m, AppOrigin: acctOrigin, Log: log, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Dashboard = dash
	return &acctE2E{e2e: h, acc: acc, bill: bill, dash: dash, mail: capture, m: m}
}

type dashSession struct{ cookie, csrf string }

// dashCall is a browser on the app host: same-origin Origin on every
// non-GET, the session cookie and CSRF header when given.
func (a *acctE2E) dashCall(method, path string, s dashSession, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, dashboard.Prefix+path, rd)
	req.Host = acctHost
	if method != http.MethodGet {
		req.Header.Set("Origin", acctOrigin)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if s.cookie != "" {
		req.AddCookie(&http.Cookie{Name: dashboard.CookieName, Value: s.cookie})
	}
	if s.csrf != "" {
		req.Header.Set(dashboard.CSRFHeader, s.csrf)
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	return rec
}

func (a *acctE2E) wantStatus(what string, rec *httptest.ResponseRecorder, status int) {
	a.t.Helper()
	if rec.Code != status {
		a.t.Fatalf("%s: %d, want %d: %s", what, rec.Code, status, rec.Body)
	}
}

func sessionOf(t *testing.T, rec *httptest.ResponseRecorder) (dashSession, int64) {
	t.Helper()
	var s dashSession
	for _, c := range rec.Result().Cookies() {
		if c.Name == dashboard.CookieName {
			s.cookie = c.Value
		}
	}
	var body struct {
		Account   struct{ ID int64 } `json:"account"`
		CSRFToken string             `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || s.cookie == "" || body.CSRFToken == "" {
		t.Fatalf("no session in %s (%v)", rec.Body, err)
	}
	s.csrf = body.CSRFToken
	return s, body.Account.ID
}

// signupAndLogin is the customer's path to a session: sign up, verify with
// the emailed code, then log in (a fresh session, as a returning customer
// has).
func (a *acctE2E) signupAndLogin(email string) (dashSession, int64) {
	a.t.Helper()
	a.wantStatus("signup", a.dashCall(http.MethodPost, "/signup", dashSession{},
		map[string]string{"email": email, "password": acctPass, "password_confirm": acctPass}), http.StatusAccepted)
	a.dash.Wait()
	codes := a.mail.of("verification-code", email)
	if len(codes) != 1 {
		a.t.Fatalf("verification emails to %s: %+v", email, codes)
	}
	m := codeRe.FindStringSubmatch(codes[0].text)
	if m == nil {
		a.t.Fatalf("no code in %q", codes[0].text)
	}
	a.wantStatus("verify", a.dashCall(http.MethodPost, "/verify", dashSession{}, map[string]string{"email": email, "code": m[1]}), http.StatusOK)
	rec := a.dashCall(http.MethodPost, "/login", dashSession{}, map[string]string{"email": email, "password": acctPass})
	a.wantStatus("login", rec, http.StatusOK)
	return sessionOf(a.t, rec)
}

type createdKey struct {
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

func (a *acctE2E) createKey(s dashSession, name string, scopes ...string) createdKey {
	a.t.Helper()
	rec := a.dashCall(http.MethodPost, "/keys", s, map[string]any{"name": name, "scopes": scopes})
	a.wantStatus("create key "+name, rec, http.StatusCreated)
	var k createdKey
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil {
		a.t.Fatal(err)
	}
	return k
}

func (a *acctE2E) listKeys(s dashSession) ([]createdKey, string) {
	a.t.Helper()
	rec := a.dashCall(http.MethodGet, "/keys", s, nil)
	a.wantStatus("list keys", rec, http.StatusOK)
	var ks []createdKey
	if err := json.Unmarshal(rec.Body.Bytes(), &ks); err != nil {
		a.t.Fatal(err)
	}
	return ks, rec.Body.String()
}

func (a *acctE2E) admin(args ...string) string {
	a.t.Helper()
	var out bytes.Buffer
	cli := &admin.CLI{DB: a.st.Pool(), Accounts: a.acc, Biller: a.bill, Mailer: a.m, Actor: "ops-e2e", Out: &out}
	if err := cli.Run(a.ctx, args); err != nil {
		a.t.Fatalf("admin %v: %v (%s)", args, err, out.String())
	}
	return out.String()
}

func (a *acctE2E) balance(accountID int64) billing.Balance {
	a.t.Helper()
	b, err := a.bill.Balance(a.ctx, accountID)
	a.must(err)
	return b
}

func (a *acctE2E) entryKinds(accountID int64) []string {
	a.t.Helper()
	es, err := a.bill.Entries(a.ctx, accountID)
	a.must(err)
	var out []string
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

func TestAccountsEndToEnd(t *testing.T) {
	if os.Getenv("COMPUTE_TEST_DATABASE_URL") == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping the end-to-end test")
	}
	t.Run("signup to settled job with a uck_ key against the local biller", testLocalKeyJobLifecycle)
	t.Run("revoked, read-only and foreign keys", testLocalKeyRefusals)
}

func testLocalKeyJobLifecycle(t *testing.T) {
	a := newAccountsE2E(t)
	const email = "ada@example.com"
	s, accountID := a.signupAndLogin(email)

	// Key creation needs a password entered in the last 10 minutes; the
	// login just now counts, so make it stale first and reauth.
	a.clock.Advance(11 * time.Minute)
	a.wantStatus("create key without reauth", a.dashCall(http.MethodPost, "/keys", s,
		map[string]any{"name": "laptop", "scopes": []string{"jobs"}}), http.StatusUnauthorized)
	a.wantStatus("reauth", a.dashCall(http.MethodPost, "/reauth", s, map[string]string{"password": acctPass}), http.StatusNoContent)
	k := a.createKey(s, "laptop", "jobs", "read")
	if !accounts.WellFormedKey(k.Secret) || k.Display != k.Secret[:12] || fmt.Sprint(k.Scopes) != "[jobs read]" || k.LastUsedAt != nil {
		t.Fatalf("created key %+v", k)
	}

	a.dash.Wait()
	created := a.mail.of("key-created", email)
	if len(created) != 1 || !strings.Contains(created[0].text, k.Display) || !strings.Contains(created[0].text, "laptop") ||
		strings.Contains(created[0].text, k.Secret) {
		t.Fatalf("key-created email %+v", created)
	}
	for _, m := range a.mail.all() {
		if strings.Contains(m.text, k.Secret) {
			t.Fatalf("the secret was emailed in %s", m.kind)
		}
	}
	if ks, raw := a.listKeys(s); len(ks) != 1 || ks[0].ID != k.ID || ks[0].Secret != "" || strings.Contains(raw, k.Secret[12:]) {
		t.Fatalf("listed keys %s", raw)
	}

	// An sk- key is not a key here at all.
	if r := a.call(http.MethodGet, "/compute/v1/jobs", "sk-alice-0000000001", ""); r.code != http.StatusUnauthorized || r.errCode() != "missing_key" {
		t.Fatalf("sk- key under local: %d %s", r.code, r.body)
	}

	// Compute is off by default (ADR-0011 D3): refused before quota and the
	// hold, and nothing moves.
	r, _ := a.submit(k.Secret, templateBody(""))
	if r.code != http.StatusForbidden || r.errCode() != "compute_not_enabled" {
		t.Fatalf("submit before enable-compute: %d %s", r.code, r.body)
	}
	if b := a.balance(accountID); b != (billing.Balance{}) || len(a.entryKinds(accountID)) != 0 {
		t.Fatalf("balance after a refused submit %+v %v", b, a.entryKinds(accountID))
	}
	if launches := a.sky.Launches(); len(launches) != 0 {
		t.Fatalf("launched %+v", launches)
	}

	id := strconv.FormatInt(accountID, 10)
	a.admin("account", "enable-compute", id, "--reason", "e2e: let ada run jobs")
	a.admin("balance", "credit", id, "--usd", "5", "--reason", "e2e: starting credit")
	credit := int64(5 * sku.QuotaPerUSD)
	if b := a.balance(accountID); b.Available != credit || b.Held != 0 {
		t.Fatalf("balance after credit %+v", b)
	}

	r, j := a.submit(k.Secret, templateBody(""))
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	if j.Usage.HoldUSD != "1.2000" || j.SKU != "a10g-24gb-x1" {
		t.Fatalf("submitted job %+v", j)
	}
	// The hold is local, against the account and the key.
	if b := a.balance(accountID); b.Held != quotaOf(3600) || b.Available != credit-quotaOf(3600) {
		t.Fatalf("balance with the hold %+v", b)
	}
	hd, err := a.svc.Ledger.Hold(a.ctx, j.ID)
	a.must(err)
	rec, ok, err := a.bill.HoldRecord(a.ctx, hd.HoldID)
	a.must(err)
	if !ok || rec.AccountID != accountID || rec.KeyID != k.ID || rec.Status != "open" {
		t.Fatalf("billing hold %+v %v", rec, ok)
	}
	stored, err := a.st.Get(a.ctx, accounts.OwnerFor(accountID), j.ID)
	a.must(err)
	if stored.Owner != "account:"+id {
		t.Fatalf("job owner %q", stored.Owner)
	}

	upAt := a.up(j.ID)
	a.runFor(12*time.Minute+30*time.Second, 30*time.Second)
	end := upAt.Add(754 * time.Second)
	a.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	a.clock.Advance(10 * time.Second)
	a.step()
	a.teardown(j.ID)

	done := a.get(k.Secret, j.ID)
	if done.Status != store.StatusSucceeded || done.Usage.GPUSeconds != 754 || done.Usage.SettledCostUSD != usd(754) || done.Usage.HoldUSD != "0.0000" {
		t.Fatalf("finished job %+v", done)
	}
	if b := a.balance(accountID); b.Held != 0 || b.Available != credit-quotaOf(754) {
		t.Fatalf("final balance %+v, want available %d (credit %d minus quota(754 s) %d), held 0", b, credit-quotaOf(754), credit, quotaOf(754))
	}
	kinds := a.entryKinds(accountID)
	t.Logf("balance entries %v", kinds)
	if len(kinds) < 4 || kinds[0] != "admin_credit" || kinds[1] != "hold" || kinds[len(kinds)-1] != "release" {
		t.Fatalf("balance entries %v", kinds)
	}
	for _, kd := range kinds[2 : len(kinds)-1] {
		if kd != "settle" {
			t.Fatalf("balance entries %v", kinds)
		}
	}
	if mm, err := a.bill.Audit(a.ctx, &accountID); err != nil || len(mm) != 0 {
		t.Fatalf("balance invariant %v %v", mm, err)
	}

	// last_used_at moved, and is written at most once a minute.
	ks, _ := a.listKeys(s)
	if ks[0].LastUsedAt == nil {
		t.Fatalf("last_used_at not recorded: %+v", ks[0])
	}
	var before time.Time
	a.must(a.st.Pool().QueryRow(a.ctx, `SELECT last_used_at FROM api_keys WHERE id = $1`, k.ID).Scan(&before))
	a.clock.Advance(10 * time.Second)
	a.get(k.Secret, j.ID)
	var after time.Time
	a.must(a.st.Pool().QueryRow(a.ctx, `SELECT last_used_at FROM api_keys WHERE id = $1`, k.ID).Scan(&after))
	if !after.Equal(before) {
		t.Fatalf("last_used_at rewritten within a minute: %v -> %v", before, after)
	}
}

func testLocalKeyRefusals(t *testing.T) {
	a := newAccountsE2E(t)
	alice, aliceID := a.signupAndLogin("alice@example.com")
	bob, bobID := a.signupAndLogin("bob@example.com")
	for _, id := range []int64{aliceID, bobID} {
		s := strconv.FormatInt(id, 10)
		a.admin("account", "enable-compute", s, "--reason", "e2e")
		a.admin("balance", "credit", s, "--usd", "5", "--reason", "e2e")
	}
	aliceJobs := a.createKey(alice, "ci", "jobs")
	aliceRead := a.createKey(alice, "dashboards", "read")
	bobJobs := a.createKey(bob, "bob", "jobs")

	r, j := a.submit(aliceJobs.Secret, templateBody(""))
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}

	// read-only: GETs work, submit and cancel are scope_denied and hold
	// nothing.
	heldBefore := a.balance(aliceID)
	if r, _ := a.submit(aliceRead.Secret, templateBody("")); r.code != http.StatusForbidden || r.errCode() != "scope_denied" {
		t.Fatalf("read key submit: %d %s", r.code, r.body)
	}
	if r := a.call(http.MethodPost, "/compute/v1/jobs/"+j.ID+"/cancel", aliceRead.Secret, ""); r.code != http.StatusForbidden || r.errCode() != "scope_denied" {
		t.Fatalf("read key cancel: %d %s", r.code, r.body)
	}
	if got := a.balance(aliceID); got != heldBefore {
		t.Fatalf("a refused submit moved the balance: %+v -> %+v", heldBefore, got)
	}
	if got := a.get(aliceRead.Secret, j.ID); got.ID != j.ID {
		t.Fatalf("read key get %+v", got)
	}
	if r := a.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/events", aliceRead.Secret, ""); r.code != http.StatusOK {
		t.Fatalf("read key events: %d %s", r.code, r.body)
	}

	// Owner isolation: bob's key sees none of alice's jobs, and bob's
	// session cannot revoke alice's key.
	if r := a.call(http.MethodGet, "/compute/v1/jobs/"+j.ID, bobJobs.Secret, ""); r.code != http.StatusNotFound || r.errCode() != "job_not_found" {
		t.Fatalf("bob gets alice's job: %d %s", r.code, r.body)
	}
	if r := a.call(http.MethodPost, "/compute/v1/jobs/"+j.ID+"/cancel", bobJobs.Secret, ""); r.code != http.StatusNotFound {
		t.Fatalf("bob cancels alice's job: %d %s", r.code, r.body)
	}
	if r := a.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/events", bobJobs.Secret, ""); r.code != http.StatusNotFound {
		t.Fatalf("bob reads alice's events: %d %s", r.code, r.body)
	}
	r = a.call(http.MethodGet, "/compute/v1/jobs", bobJobs.Secret, "")
	var list struct{ Data []jobView }
	r.json(t, &list)
	if r.code != http.StatusOK || len(list.Data) != 0 {
		t.Fatalf("bob's list: %d %s", r.code, r.body)
	}
	a.wantStatus("bob revokes alice's key", a.dashCall(http.MethodDelete, "/keys/"+strconv.FormatInt(aliceJobs.ID, 10), bob, nil), http.StatusNotFound)
	if ks, _ := a.listKeys(bob); len(ks) != 1 || ks[0].ID != bobJobs.ID {
		t.Fatalf("bob's keys %+v", ks)
	}

	// Revocation is effective on the very next request: no cache.
	if r := a.call(http.MethodGet, "/compute/v1/jobs", aliceJobs.Secret, ""); r.code != http.StatusOK {
		t.Fatalf("before revoke: %d %s", r.code, r.body)
	}
	a.wantStatus("revoke", a.dashCall(http.MethodDelete, "/keys/"+strconv.FormatInt(aliceJobs.ID, 10), alice, nil), http.StatusNoContent)
	if r := a.call(http.MethodGet, "/compute/v1/jobs", aliceJobs.Secret, ""); r.code != http.StatusUnauthorized || r.errCode() != "invalid_key" {
		t.Fatalf("after revoke: %d %s", r.code, r.body)
	}
	if r, _ := a.submit(aliceJobs.Secret, templateBody("")); r.code != http.StatusUnauthorized || r.errCode() != "invalid_key" {
		t.Fatalf("submit after revoke: %d %s", r.code, r.body)
	}
	a.wantStatus("revoke again", a.dashCall(http.MethodDelete, "/keys/"+strconv.FormatInt(aliceJobs.ID, 10), alice, nil), http.StatusNoContent)
	ks, _ := a.listKeys(alice)
	if len(ks) != 2 || ks[1].ID != aliceJobs.ID || ks[1].RevokedAt == nil || ks[0].RevokedAt != nil {
		t.Fatalf("alice's keys after revoke %+v", ks)
	}

	// A made-up key of the right shape is invalid_key; a wrong shape is
	// missing_key without a lookup.
	if r := a.call(http.MethodGet, "/compute/v1/jobs", "uck_"+strings.Repeat("A", 40), ""); r.code != http.StatusUnauthorized || r.errCode() != "invalid_key" {
		t.Fatalf("unknown key: %d %s", r.code, r.body)
	}
	for _, bad := range []string{"uck_short", "uck_" + strings.Repeat("A", 41), "uck_" + strings.Repeat("-", 40)} {
		if r := a.call(http.MethodGet, "/compute/v1/jobs", bad, ""); r.code != http.StatusUnauthorized || r.errCode() != "missing_key" {
			t.Fatalf("malformed %q: %d %s", bad, r.code, r.body)
		}
	}

	// The job alice's revoked key submitted still belongs to her account
	// and can be cancelled with another jobs key.
	other := a.createKey(alice, "ci-2", "jobs")
	if r := a.call(http.MethodPost, "/compute/v1/jobs/"+j.ID+"/cancel", other.Secret, ""); r.code != http.StatusAccepted {
		t.Fatalf("cancel with another key: %d %s", r.code, r.body)
	}
}

// With the console backend nothing about sk- keys changes, and a uck_ key
// is refused as invalid_key without a console call, wired or dark.
func TestUCKKeyRefusedWithoutLocalAccounts(t *testing.T) {
	key := "uck_" + strings.Repeat("a", 40)
	dark := newTestServer(t, "aws")
	if rec := do(t, dark, http.MethodGet, "/compute/v1/jobs", key, ""); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"invalid_key"`) {
		t.Errorf("dark: uck_ key = %d %s", rec.Code, rec.Body)
	}
	wired := newTestServer(t, "aws")
	wired.Verifier = &Verifier{}
	// The verifier has no console at all: reaching it would panic, so a
	// clean 401 proves the console was not asked.
	rec := do(t, wired, http.MethodPost, "/compute/v1/jobs", key, validSubmit)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"invalid_key"`) {
		t.Errorf("wired: uck_ key = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, dark, http.MethodGet, "/compute/v1/jobs", "sk-abcdefghij", ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("dark: sk- key = %d %s", rec.Code, rec.Body)
	}
}

// stubKeys is a KeyVerifier without a database.
type stubKeys map[string]accounts.VerifiedKey

func (s stubKeys) VerifyKey(_ context.Context, key string) (accounts.VerifiedKey, error) {
	if vk, ok := s[key]; ok {
		return vk, nil
	}
	return accounts.VerifiedKey{}, accounts.ErrKeyRejected
}

// Scope enforcement and the per-key rate limit's bucket, without a job
// store: a read key may only GET, and two keys of one account have
// separate budgets because they count on the key id.
func TestLocalKeyScopesAndRateBuckets(t *testing.T) {
	read := "uck_" + strings.Repeat("r", 40)
	jobsKey := "uck_" + strings.Repeat("j", 40)
	s := newTestServer(t, "aws")
	s.Keys = stubKeys{
		read:    {KeyID: 7, AccountID: 3, Scopes: []string{accounts.ScopeRead}},
		jobsKey: {KeyID: 8, AccountID: 3, Scopes: []string{accounts.ScopeJobs}},
	}
	s.Owner = KeyOwner(s.Keys)
	for _, c := range []struct {
		method, path, key string
		status            int
		code              string
	}{
		{http.MethodPost, "/compute/v1/jobs", read, 403, "scope_denied"},
		{http.MethodPost, "/compute/v1/jobs/job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B/cancel", read, 403, "scope_denied"},
		{http.MethodGet, "/compute/v1/jobs", read, 501, "not_implemented"},
		{http.MethodPost, "/compute/v1/jobs", jobsKey, 501, "not_implemented"},
		{http.MethodGet, "/compute/v1/jobs", "sk-abcdefghij", 401, "missing_key"},
		{http.MethodGet, "/compute/v1/jobs", "", 401, "missing_key"},
		{http.MethodGet, "/compute/v1/jobs", "uck_" + strings.Repeat("x", 40), 401, "invalid_key"},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(validSubmit))
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		_, pattern := s.mux.Handler(req)
		conformsToOpenAPI(t, pattern, c.method+" "+c.path, rec)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"`+c.code+`"`) {
			t.Errorf("%s %s with %.8s: %d %s, want %d %s", c.method, c.path, c.key, rec.Code, rec.Body, c.status, c.code)
		}
	}

	ctx := withCaller(context.Background(), caller{owner: "account:3", rateKey: "key:7"})
	other := withCaller(context.Background(), caller{owner: "account:3", rateKey: "key:8"})
	s.reads = ratelimit.New(1, time.Hour)
	h := s.limit(s.reads, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	hit := func(ctx context.Context, rawKey string) int {
		req := httptest.NewRequest(http.MethodGet, "/compute/v1/jobs", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if hit(ctx, read) != http.StatusNoContent || hit(ctx, jobsKey) != http.StatusTooManyRequests {
		t.Error("the bucket follows the raw key, not the key id")
	}
	if hit(other, read) != http.StatusNoContent {
		t.Error("a second key of the same account shares the first key's bucket")
	}
}
