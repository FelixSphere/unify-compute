package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/dashboard"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// The dashboard's session routes over the balance, its history, usage and
// the account's jobs (task A7d), end to end on the ACCOUNTS_BACKEND=local
// harness: the same wiring as main, with the dashboard given the job API's
// handlers (Server.AccountJobs) and the local biller as its ledger.

// newDashboardE2E is newAccountsE2E with the dashboard wired as main wires
// it since A7d.
func newDashboardE2E(t *testing.T) *acctE2E {
	t.Helper()
	a := newAccountsE2E(t)
	dash, err := dashboard.New(dashboard.Config{Accounts: a.acc, Mailer: a.m, AppOrigin: acctOrigin,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: a.clock.Now, Jobs: a.srv.AccountJobs(), Ledger: a.bill})
	if err != nil {
		t.Fatal(err)
	}
	a.srv.Dashboard, a.dash = dash, dash
	return a
}

// dashGet is a dashboard GET that must answer 200; it decodes into v when v
// is not nil and returns the raw body.
func (a *acctE2E) dashGet(path string, s dashSession, v any) string {
	a.t.Helper()
	rec := a.dashCall(http.MethodGet, path, s, nil)
	a.wantStatus("GET "+path, rec, http.StatusOK)
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			a.t.Fatalf("GET %s: %v: %s", path, err, rec.Body)
		}
	}
	return rec.Body.String()
}

func dashErr(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	r := resp{code: rec.Code, body: rec.Body.Bytes()}
	if r.code != status || r.errCode() != code {
		t.Fatalf("%s: %d %s, want %d %s", what, r.code, r.body, status, code)
	}
}

// sameJSON says whether two bodies decode to the same value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}

type dashBalance struct {
	Object       string `json:"object"`
	Currency     string `json:"currency"`
	AvailableUSD string `json:"available_usd"`
	HeldUSD      string `json:"held_usd"`
}

type dashEntry struct {
	ID                string  `json:"id"`
	Kind              string  `json:"kind"`
	AmountUSD         string  `json:"amount_usd"`
	AvailableAfterUSD string  `json:"available_after_usd"`
	HeldAfterUSD      string  `json:"held_after_usd"`
	JobID             *string `json:"job_id"`
	Ref               *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"ref"`
	Detail map[string]string `json:"detail"`
}

type dashEntries struct {
	Data    []dashEntry `json:"data"`
	HasMore bool        `json:"has_more"`
	Next    string      `json:"next"`
}

type dashUsage struct {
	Month      string `json:"month"`
	GPUSeconds int64  `json:"gpu_seconds"`
	GPUUSD     string `json:"gpu_usd"`
	StorageUSD string `json:"storage_usd"`
	EgressUSD  string `json:"egress_usd"`
	TotalUSD   string `json:"total_usd"`
	BySKU      []struct {
		SKU        string `json:"sku"`
		GPUSeconds int64  `json:"gpu_seconds"`
		USD        string `json:"usd"`
	} `json:"by_sku"`
}

func (a *acctE2E) history(s dashSession) []dashEntry {
	a.t.Helper()
	var l dashEntries
	a.dashGet("/transactions?limit=100", s, &l)
	if l.HasMore {
		a.t.Fatal("history longer than one page of 100")
	}
	return l.Data
}

func rat(t *testing.T, usd string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(usd)
	if !ok {
		t.Fatalf("not a decimal: %q", usd)
	}
	return r
}

func thisMonth() string { return time.Now().UTC().Format("2006-01") }

func TestDashboardEndToEnd(t *testing.T) {
	if os.Getenv("COMPUTE_TEST_DATABASE_URL") == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping the end-to-end test")
	}
	t.Run("signup to a settled job, seen through the dashboard", testDashboardJobLifecycle)
	t.Run("owner isolation and cancel with CSRF", testDashboardIsolationAndCancel)
}

func testDashboardJobLifecycle(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "sha256sum", "find"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	a := newDashboardE2E(t)
	const email = "ada@example.com"
	s, accountID := a.signupAndLogin(email)

	// A new account: nothing anywhere, all of it in decimal strings.
	var b dashBalance
	a.dashGet("/balance", s, &b)
	if b != (dashBalance{Object: "compute.balance", Currency: "USD", AvailableUSD: "0.0000", HeldUSD: "0.0000"}) {
		t.Fatalf("new balance %+v", b)
	}
	if h := a.history(s); len(h) != 0 {
		t.Fatalf("new history %+v", h)
	}
	var jl struct{ Data []jobView }
	a.dashGet("/jobs", s, &jl)
	if len(jl.Data) != 0 {
		t.Fatalf("new jobs %+v", jl)
	}

	id := strconv.FormatInt(accountID, 10)
	a.admin("account", "enable-compute", id, "--reason", "e2e: let ada run jobs")
	a.admin("balance", "credit", id, "--usd", "5", "--reason", "e2e: starting credit")
	credit := int64(5 * sku.QuotaPerUSD)
	k := a.createKey(s, "laptop", "jobs")

	mount := filepath.Join(t.TempDir(), "out")
	cmd := fmt.Sprintf(`printf 'line 1\n' > %s/results.jsonl && echo done`, mount)
	body := fmt.Sprintf(`{"sku":"a10g-24gb-x1","spec":{"image":"ghcr.io/acme/embed:1.4","command":["bash","-c",%q],"outputs":[{"mount":%q}],"max_runtime_seconds":3600}}`, cmd, mount)
	r, j := a.submit(k.Secret, body)
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	a.dashGet("/balance", s, &b)
	if b.HeldUSD != quotaUSD(quotaOf(3600)) || b.AvailableUSD != quotaUSD(credit-quotaOf(3600)) {
		t.Fatalf("balance with the hold %+v", b)
	}

	// Running: the logs the cluster has are the dashboard's, as the job
	// API's.
	upAt := a.up(j.ID)
	a.sky.SetLogs(cluster(j.ID), "loading model\nstep 2 of 9\n")
	logs := a.dashCall(http.MethodGet, "/jobs/"+j.ID+"/logs", s, nil)
	api := a.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/logs", k.Secret, "")
	if logs.Code != http.StatusOK || logs.Body.String() != "loading model\nstep 2 of 9\n" || logs.Body.String() != string(api.body) ||
		logs.Header().Get("Content-Type") != api.hdr.Get("Content-Type") {
		t.Fatalf("dashboard logs %d %q (%s), job API %q", logs.Code, logs.Body, logs.Header().Get("Content-Type"), api.body)
	}
	var running struct{ Data []jobView }
	a.dashGet("/jobs?status=running", s, &running)
	if len(running.Data) != 1 || running.Data[0].ID != j.ID {
		t.Fatalf("running jobs %+v", running)
	}

	// The node's own run and upload step, for real against compute-api and
	// the fake S3.
	var task struct {
		Run  string            `json:"run"`
		Envs map[string]string `json:"envs"`
	}
	for _, l := range a.sky.Launches() {
		if l.Cluster == cluster(j.ID) {
			a.must(json.Unmarshal([]byte(l.TaskRaw), &task))
		}
	}
	if task.Envs[artifacts.EnvToken] == "" {
		t.Fatalf("task has no upload token: %+v", task)
	}
	c := exec.Command("bash", "-c", task.Run)
	c.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range task.Envs {
		c.Env = append(c.Env, k+"="+v)
	}
	if out, err := c.CombinedOutput(); err != nil || !strings.Contains(string(out), "1 output file(s) processed") {
		t.Fatalf("run step: %v\n%s", err, out)
	}

	a.runFor(12*time.Minute+30*time.Second, 30*time.Second)
	end := upAt.Add(754 * time.Second)
	a.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	a.clock.Advance(10 * time.Second)
	a.step()
	a.teardown(j.ID)
	done := a.get(k.Secret, j.ID)
	if done.Status != store.StatusSucceeded || done.Usage.SettledCostUSD != usd(754) || done.Usage.HoldUSD != "0.0000" {
		t.Fatalf("finished job %+v", done)
	}

	// The job, its list, events and artifacts: the job API's bodies.
	for _, p := range []string{"", "/events", "/events?limit=2", "/artifacts"} {
		d := a.dashCall(http.MethodGet, "/jobs/"+j.ID+p, s, nil)
		api := a.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+p, k.Secret, "")
		if d.Code != http.StatusOK || api.code != http.StatusOK {
			t.Fatalf("%s: dashboard %d %s, job API %d %s", p, d.Code, d.Body, api.code, api.body)
		}
		// Presigned URLs carry their signing time; everything else must
		// match exactly.
		if p == "/artifacts" {
			continue
		}
		if !sameJSON(t, d.Body.Bytes(), api.body) {
			t.Fatalf("%s differs:\ndashboard %s\njob API   %s", p, d.Body, api.body)
		}
	}
	for q, n := range map[string]int{"": 1, "?status=succeeded": 1, "?status=running": 0, "?status=succeeded&status=failed&limit=1": 1} {
		var l struct{ Data []jobView }
		a.dashGet("/jobs"+q, s, &l)
		if len(l.Data) != n || (n == 1 && l.Data[0].ID != j.ID) {
			t.Fatalf("/jobs%s: %+v", q, l)
		}
	}
	dashErr(t, "bad status", a.dashCall(http.MethodGet, "/jobs?status=finished", s, nil), http.StatusBadRequest, "invalid_status")
	var evs eventsList
	a.dashGet("/jobs/"+j.ID+"/events", s, &evs)
	types := make([]string, len(evs.Data))
	for i, e := range evs.Data {
		types[i] = e.Type
	}
	if len(types) < 2 || types[0] != "job.queued" || types[len(types)-1] != "job.succeeded" {
		t.Fatalf("events %v", types)
	}
	var arts struct {
		Data     []artifactView `json:"data"`
		Complete bool           `json:"complete"`
	}
	a.dashGet("/jobs/"+j.ID+"/artifacts", s, &arts)
	if !arts.Complete || len(arts.Data) != 1 || arts.Data[0].Path != mount+"/results.jsonl" {
		t.Fatalf("artifacts %+v", arts)
	}
	got, err := a.s3.Server.Client().Get(arts.Data[0].URL)
	a.must(err)
	content, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != http.StatusOK || string(content) != "line 1\n" {
		t.Fatalf("download from the dashboard's URL: %d %q", got.StatusCode, content)
	}

	// The balance and its history: credit, hold, the settles, the release.
	a.dashGet("/balance", s, &b)
	if b.AvailableUSD != quotaUSD(credit-quotaOf(754)) || b.HeldUSD != "0.0000" {
		t.Fatalf("final balance %+v, want available %s", b, quotaUSD(credit-quotaOf(754)))
	}
	hist := a.history(s)
	raw := a.dashGet("/transactions?limit=100", s, nil)
	if strings.Contains(raw, "starting credit") || strings.Contains(raw, "ae_") || strings.Contains(raw, "hold_") {
		t.Fatalf("history exposes an operator reason, an audit id or a hold id: %s", raw)
	}
	kinds := make([]string, len(hist))
	for i, e := range hist {
		kinds[len(hist)-1-i] = e.Kind
	}
	t.Logf("history, oldest first: %v", kinds)
	if len(kinds) < 4 || kinds[0] != "admin_credit" || kinds[1] != "hold" || kinds[len(kinds)-1] != "release" {
		t.Fatalf("history %v", kinds)
	}
	charged := new(big.Rat)
	for _, e := range hist {
		switch e.Kind {
		case "admin_credit":
			if e.Ref != nil || e.JobID != nil || len(e.Detail) != 0 || e.AmountUSD != "5.0000" {
				t.Fatalf("credit entry %+v", e)
			}
		case "hold", "settle", "release":
			if e.JobID == nil || *e.JobID != j.ID || e.Ref == nil || e.Ref.Type != "job" || e.Ref.ID != j.ID || e.Detail["sku"] != "a10g-24gb-x1" {
				t.Fatalf("%s entry %+v", e.Kind, e)
			}
			if e.Kind == "settle" {
				charged.Add(charged, rat(t, e.AmountUSD))
			}
		default:
			t.Fatalf("unexpected entry %+v", e)
		}
	}
	if charged.Neg(charged).Cmp(rat(t, done.Usage.SettledCostUSD)) != 0 {
		t.Fatalf("settles charged %s, the job settled %s", charged.FloatString(4), done.Usage.SettledCostUSD)
	}
	if hist[0].AvailableAfterUSD != b.AvailableUSD || hist[0].HeldAfterUSD != "0.0000" {
		t.Fatalf("newest entry %+v, balance %+v", hist[0], b)
	}

	// Pages of two walk the same history; a foreign or made-up cursor is
	// refused.
	var walked []string
	for q := "/transactions?limit=2"; ; {
		var page dashEntries
		a.dashGet(q, s, &page)
		for _, e := range page.Data {
			walked = append(walked, e.ID)
		}
		if !page.HasMore {
			break
		}
		if page.Next != page.Data[len(page.Data)-1].ID {
			t.Fatalf("next %q is not the last id of %+v", page.Next, page.Data)
		}
		q = "/transactions?limit=2&after=" + page.Next
	}
	var all []string
	for _, e := range hist {
		all = append(all, e.ID)
	}
	if fmt.Sprint(walked) != fmt.Sprint(all) {
		t.Fatalf("paged %v, whole %v", walked, all)
	}
	dashErr(t, "made-up cursor", a.dashCall(http.MethodGet, "/transactions?after=be_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", s, nil), http.StatusBadRequest, "invalid_cursor")

	// Usage: the month's total is exactly what the job settled.
	var u dashUsage
	a.dashGet("/usage?month="+thisMonth(), s, &u)
	if u.Month != thisMonth() || u.TotalUSD != done.Usage.SettledCostUSD || u.GPUUSD != u.TotalUSD || u.GPUSeconds != 754 ||
		u.StorageUSD != "0.0000" || u.EgressUSD != "0.0000" || len(u.BySKU) != 1 ||
		u.BySKU[0].SKU != "a10g-24gb-x1" || u.BySKU[0].GPUSeconds != 754 || u.BySKU[0].USD != usd(754) {
		t.Fatalf("usage %+v, want total %s over 754 s", u, done.Usage.SettledCostUSD)
	}
	var plain dashUsage
	a.dashGet("/usage", s, &plain)
	if plain.Month != a.clock.Now().UTC().Format("2006-01") {
		t.Fatalf("usage without month answered %s", plain.Month)
	}
	prev := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	a.dashGet("/usage?month="+prev, s, &u)
	if u.TotalUSD != "0.0000" || u.GPUSeconds != 0 || len(u.BySKU) != 0 {
		t.Fatalf("previous month %+v", u)
	}
	dashErr(t, "bad month", a.dashCall(http.MethodGet, "/usage?month=2026-1", s, nil), http.StatusBadRequest, "invalid_month")

	// Sessions and keys never cross: the key is no credential here, the
	// cookie none on the job API.
	keyed := httptest.NewRequest(http.MethodGet, dashboard.Prefix+"/jobs/"+j.ID, nil)
	keyed.Host = acctHost
	keyed.Header.Set("Authorization", "Bearer "+k.Secret)
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, keyed)
	dashErr(t, "dashboard with an API key", rec, http.StatusUnauthorized, "session_required")
	cookied := httptest.NewRequest(http.MethodGet, "/compute/v1/jobs/"+j.ID, nil)
	cookied.AddCookie(&http.Cookie{Name: dashboard.CookieName, Value: s.cookie})
	rec = httptest.NewRecorder()
	a.srv.ServeHTTP(rec, cookied)
	dashErr(t, "job API with a session cookie", rec, http.StatusUnauthorized, "missing_key")
	for _, p := range []string{"/balance", "/transactions", "/usage", "/jobs", "/jobs/" + j.ID, "/jobs/" + j.ID + "/events", "/jobs/" + j.ID + "/logs", "/jobs/" + j.ID + "/artifacts"} {
		dashErr(t, "no session "+p, a.dashCall(http.MethodGet, p, dashSession{}, nil), http.StatusUnauthorized, "session_required")
	}
}

func testDashboardIsolationAndCancel(t *testing.T) {
	a := newDashboardE2E(t)
	alice, aliceID := a.signupAndLogin("alice@example.com")
	bob, bobID := a.signupAndLogin("bob@example.com")
	for _, id := range []int64{aliceID, bobID} {
		s := strconv.FormatInt(id, 10)
		a.admin("account", "enable-compute", s, "--reason", "e2e")
		a.admin("balance", "credit", s, "--usd", "5", "--reason", "e2e")
	}
	k := a.createKey(alice, "ci", "jobs")
	r, j := a.submit(k.Secret, plainBody(3600))
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}

	// Bob's session meets alice's job exactly as it meets one that does not
	// exist: same status, same code, same message but for the id.
	const missing = "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B"
	for _, c := range []struct{ method, suffix string }{
		{http.MethodGet, ""}, {http.MethodGet, "/events"}, {http.MethodGet, "/logs"}, {http.MethodGet, "/artifacts"}, {http.MethodPost, "/cancel"},
	} {
		theirs := a.dashCall(c.method, "/jobs/"+j.ID+c.suffix, bob, nil)
		none := a.dashCall(c.method, "/jobs/"+missing+c.suffix, bob, nil)
		dashErr(t, "bob "+c.method+" alice's job"+c.suffix, theirs, http.StatusNotFound, "job_not_found")
		if strings.ReplaceAll(theirs.Body.String(), j.ID, missing) != none.Body.String() {
			t.Fatalf("%s %s: alice's job %s, a missing one %s", c.method, c.suffix, theirs.Body, none.Body)
		}
	}
	var bobJobs struct{ Data []jobView }
	a.dashGet("/jobs", bob, &bobJobs)
	if len(bobJobs.Data) != 0 {
		t.Fatalf("bob lists %+v", bobJobs)
	}
	if h := a.history(bob); len(h) != 1 || h[0].Kind != "admin_credit" {
		t.Fatalf("bob's history %+v", h)
	}
	var u dashUsage
	a.dashGet("/usage", bob, &u)
	if u.TotalUSD != "0.0000" {
		t.Fatalf("bob's usage %+v", u)
	}
	var b dashBalance
	a.dashGet("/balance", bob, &b)
	if b.AvailableUSD != "5.0000" || b.HeldUSD != "0.0000" {
		t.Fatalf("bob's balance %+v", b)
	}
	if got := a.get(k.Secret, j.ID); got.Status.Terminal() {
		t.Fatalf("bob's cancel ended alice's job: %+v", got)
	}

	// Alice cancels from the dashboard: the CSRF header and a same-origin
	// Origin are required, then it is the job API's 202.
	dashErr(t, "cancel without X-CSRF-Token", a.dashCall(http.MethodPost, "/jobs/"+j.ID+"/cancel", dashSession{cookie: alice.cookie}, nil),
		http.StatusForbidden, "csrf_failed")
	dashErr(t, "cancel with a wrong token", a.dashCall(http.MethodPost, "/jobs/"+j.ID+"/cancel", dashSession{cookie: alice.cookie, csrf: bob.csrf}, nil),
		http.StatusForbidden, "csrf_failed")
	foreign := httptest.NewRequest(http.MethodPost, dashboard.Prefix+"/jobs/"+j.ID+"/cancel", http.NoBody)
	foreign.Host = acctHost
	foreign.Header.Set("Origin", "https://evil.example")
	foreign.Header.Set(dashboard.CSRFHeader, alice.csrf)
	foreign.AddCookie(&http.Cookie{Name: dashboard.CookieName, Value: alice.cookie})
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, foreign)
	dashErr(t, "cancel from a foreign Origin", rec, http.StatusForbidden, "csrf_failed")
	if got := a.get(k.Secret, j.ID); got.Status.Terminal() {
		t.Fatalf("a refused cancel ended the job: %+v", got)
	}

	rec = a.dashCall(http.MethodPost, "/jobs/"+j.ID+"/cancel", alice, nil)
	a.wantStatus("cancel", rec, http.StatusAccepted)
	var cancelled jobView
	a.must(json.Unmarshal(rec.Body.Bytes(), &cancelled))
	if cancelled.ID != j.ID || cancelled.Status != store.StatusCancelled {
		t.Fatalf("cancel answered %s", rec.Body)
	}
	dashErr(t, "cancel again", a.dashCall(http.MethodPost, "/jobs/"+j.ID+"/cancel", alice, nil), http.StatusConflict, "job_terminal")

	for i := 0; i < 5; i++ {
		a.dashGet("/balance", alice, &b)
		if b.HeldUSD == "0.0000" {
			break
		}
		a.step()
	}
	if b.AvailableUSD != "5.0000" || b.HeldUSD != "0.0000" {
		t.Fatalf("alice's balance after a cancel before any GPU time %+v", b)
	}
	hist := a.history(alice)
	kinds := make([]string, len(hist))
	for i, e := range hist {
		kinds[len(hist)-1-i] = e.Kind
		if e.Kind != "admin_credit" && (e.JobID == nil || *e.JobID != j.ID) {
			t.Fatalf("entry without its job %+v", e)
		}
	}
	if fmt.Sprint(kinds) != "[admin_credit hold release]" {
		t.Fatalf("alice's history %v", kinds)
	}
	var events eventsList
	a.dashGet("/jobs/"+j.ID+"/events", alice, &events)
	if last := events.Data[len(events.Data)-1]; last.Type != "job.cancelled" {
		t.Fatalf("last event %+v", last)
	}
}
