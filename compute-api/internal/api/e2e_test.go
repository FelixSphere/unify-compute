package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/events"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

// The end-to-end tests run the binary's wiring (jobs.New, the Verifier, the
// shipped catalogue, templates and quotas) against the real job store in
// Postgres, the fake console (consoletest), the fake SkyPilot API server
// (skypilottest) and the fake S3 (awsv4test), on one fake clock. Time moves
// only when a test moves it; the backend loop, the ledger tick and the
// webhook worker run only when a test calls them.
//
// Every response a test receives is held against openapi.json as it
// arrives: its status is documented for the route, its content type too,
// the body fits the schema, and an error's type/code pair is listed in
// Error.x-error-codes. TestEndToEnd then checks that every listed code was
// produced either by some scenario or by the database-less servers of
// TestHandlerResponsesMatchOpenAPI.

const (
	e2eSecret = "0123456789abcdef0123456789abcdef"
	aliceKey  = "sk-alice-0000000001"
	bobKey    = "sk-bob-00000000002"
	carolKey  = "sk-carol-000000003"
	daveKey   = "sk-dave-0000000004"
	erinKey   = "sk-erin-0000000005"
	alice     = "user:1001"
	bob       = "user:1002"
	upRes     = "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)"
	// testRate is the shipped catalogue's placeholder A10G price.
	testRate = "1.2000"
)

type e2eClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *e2eClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *e2eClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type hook struct {
	Header http.Header
	Body   []byte
}

type e2e struct {
	t       *testing.T
	ctx     context.Context
	clock   *e2eClock
	st      *store.Store
	console *consoletest.Server
	sky     *skypilottest.Server
	s3      *awsv4test.Fake
	svc     *jobs.Service
	srv     *Server
	http    *httptest.Server

	hookMu sync.Mutex
	hooks  []hook
	hookSv *httptest.Server
}

type e2eOptions struct {
	noArtifacts bool
	noSkyPilot  bool
}

func newE2E(t *testing.T, opts ...e2eOptions) *e2e {
	t.Helper()
	var opt e2eOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	st := storetest.New(t)
	ctx := context.Background()
	// Ahead of the wall clock: rows the database stamps with now() (event
	// times, delivery due times) must never be in the fake clock's future.
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

	fc := consoletest.New([]byte(e2eSecret))
	t.Cleanup(fc.Close)
	fc.SetNow(clock.Now)
	for _, u := range []consoletest.User{
		{ID: 1001, Quota: 1_000_000_000}, {ID: 1002, Quota: 1_000_000_000}, {ID: 1003, Quota: 100_000},
		{ID: 1004, Quota: 1_000_000_000, Disabled: true}, {ID: 1005, TenantID: 7},
	} {
		fc.SeedUser(u)
	}
	fc.SeedTenant(consoletest.Tenant{ID: 7, Quota: 1_000_000_000, Suspended: true})
	for i, k := range []string{aliceKey, bobKey, carolKey, daveKey, erinKey} {
		fc.SeedKey(consoletest.Key{Key: k, TokenID: 11 + i, UserID: 1001 + i, Unlimited: true})
	}

	sky := skypilottest.New()
	sky.Token = "sky_e2e_token"
	t.Cleanup(sky.Close)
	sky.SetClock(clock.Now)

	h := &e2e{t: t, ctx: ctx, clock: clock, st: st, console: fc, sky: sky}
	h.srv = New("e2e", cat, reg, log)
	h.srv.Templates = tpl
	h.http = httptest.NewServer(h.srv)
	t.Cleanup(h.http.Close)

	cfg := jobs.Config{
		Store: st, Console: fc.Client(), Catalogue: cat, Log: log, Now: clock.Now,
		Quota: &quota.Enforcer{Config: quotas, Catalogue: cat, Usage: st},
	}
	if !opt.noSkyPilot {
		cfg.SkyPilot = &backend.Client{BaseURL: sky.URL, Token: sky.Token, UserHash: "unifycmp"}
	}
	if !opt.noArtifacts {
		h.s3 = awsv4test.New(t, "unify-compute-e2e-artifacts")
		art, err := artifacts.New(artifacts.Config{
			Bucket: h.s3.Bucket, WriterRoleARN: "arn:aws:iam::123456789012:role/unify-compute-e2e-artifacts-writer",
			PublicURL: h.http.URL, TokenSecret: []byte(strings.Repeat("t", 32)),
			Creds: awsv4.Static(h.s3.Instance), Client: h.s3.Client(),
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Outputs = art
		h.srv.Artifacts = art
	}
	h.svc = jobs.New(cfg)
	verifier := &Verifier{Console: fc.Client(), Log: log, Now: clock.Now}
	h.srv.Verifier, h.srv.JobService, h.srv.Now = verifier, h.svc, clock.Now
	h.srv.Owner, h.srv.Events, h.srv.Jobs = verifier.Owner, st, st

	h.hookSv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.hookMu.Lock()
		h.hooks = append(h.hooks, hook{Header: r.Header.Clone(), Body: b})
		h.hookMu.Unlock()
	}))
	t.Cleanup(h.hookSv.Close)
	return h
}

func (h *e2e) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

// --- HTTP, checked against the contract --------------------------------------

var (
	e2eSeenMu sync.Mutex
	e2eSeen   = map[string]bool{}
)

type resp struct {
	code int
	hdr  http.Header
	body []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r resp) errCode() string {
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

func (h *e2e) call(method, path, key, body string, hdr ...string) resp {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	_, pattern := h.srv.mux.Handler(req)
	conformsToOpenAPI(h.t, pattern, method+" "+path, rec)
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.Bytes()}
}

func conformsToOpenAPI(t *testing.T, pattern, where string, rec *httptest.ResponseRecorder) {
	t.Helper()
	doc := loadSpec(t)
	op, ok := specOperations(t, doc)[pattern]
	if !ok {
		t.Errorf("%s: pattern %q not documented", where, pattern)
		return
	}
	r, ok := op["responses"].(map[string]any)[strconv.Itoa(rec.Code)]
	if !ok {
		t.Errorf("%s -> %d: status not documented for %s (%s)", where, rec.Code, pattern, rec.Body)
		return
	}
	r, _ = deref(doc, r)
	ct := rec.Header().Get("Content-Type")
	content, _ := r.(map[string]any)["content"].(map[string]any)
	media, ok := content[ct].(map[string]any)
	if !ok {
		t.Errorf("%s -> %d: content type %q not documented", where, rec.Code, ct)
		return
	}
	if !strings.HasPrefix(ct, "application/json") {
		return
	}
	var body any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Errorf("%s -> %d: body is not JSON: %v", where, rec.Code, err)
		return
	}
	for _, e := range validate(doc, media["schema"], body, "body") {
		t.Errorf("%s -> %d: %s", where, rec.Code, e)
	}
	if env, ok := body.(map[string]any)["error"].(map[string]any); ok && rec.Code >= 400 {
		pair := fmt.Sprintf("%v/%v", env["type"], env["code"])
		if !errorCodes(t, doc)[pair] {
			t.Errorf("%s -> %d: error %s is not listed in Error.x-error-codes", where, rec.Code, pair)
		}
		e2eSeenMu.Lock()
		e2eSeen[pair] = true
		e2eSeenMu.Unlock()
	}
}

func (h *e2e) submit(key, body string, hdr ...string) (resp, jobView) {
	h.t.Helper()
	r := h.call(http.MethodPost, "/compute/v1/jobs", key, body, hdr...)
	var j jobView
	if r.code == http.StatusCreated || r.code == http.StatusOK {
		r.json(h.t, &j)
	}
	return r, j
}

func (h *e2e) get(key, id string) jobView {
	h.t.Helper()
	r := h.call(http.MethodGet, "/compute/v1/jobs/"+id, key, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("get %s: %d %s", id, r.code, r.body)
	}
	var j jobView
	r.json(h.t, &j)
	return j
}

func (h *e2e) eventTypes(key, id string) []string {
	h.t.Helper()
	r := h.call(http.MethodGet, "/compute/v1/jobs/"+id+"/events?limit=100", key, "")
	if r.code != http.StatusOK {
		h.t.Fatalf("events %s: %d %s", id, r.code, r.body)
	}
	var l eventsList
	r.json(h.t, &l)
	var out []string
	for _, e := range l.Data {
		out = append(out, e.Type)
	}
	return out
}

func (h *e2e) event(key, id, typ string) events.Object {
	h.t.Helper()
	r := h.call(http.MethodGet, "/compute/v1/jobs/"+id+"/events?limit=100", key, "")
	var l eventsList
	r.json(h.t, &l)
	for _, e := range l.Data {
		if e.Type == typ {
			return e
		}
	}
	h.t.Fatalf("job %s has no %s event", id, typ)
	return events.Object{}
}

// --- driving time ------------------------------------------------------------

// step is one backend round (every attempt observed once, the ledger's stop
// requests carried out) and one ledger tick.
func (h *e2e) step() {
	h.t.Helper()
	h.svc.Step(h.ctx)
	if err := h.svc.Ledger.Tick(h.ctx); err != nil {
		h.t.Logf("ledger tick: %v", err)
	}
}

// runFor advances the clock in increments of every, stepping after each.
func (h *e2e) runFor(d, every time.Duration) {
	h.t.Helper()
	for end := h.clock.Now().Add(d); h.clock.Now().Before(end); {
		h.clock.Advance(every)
		h.step()
	}
}

func cluster(id string) string { return backend.ClusterName(id, 1) }

// up drives a provisioning job to running: provisioned, then UP after a
// minute (never billed), with its job submitted on the node. It returns the
// moment compute-api observed UP, which is where billing starts.
func (h *e2e) up(id string) time.Time {
	h.t.Helper()
	h.sky.Provision(cluster(id))
	h.step()
	h.clock.Advance(time.Minute)
	h.sky.Up(cluster(id), "AWS", "us-east-1", upRes)
	upAt := h.clock.Now()
	h.step()
	h.sky.SubmitJob(cluster(id))
	start := upAt.Add(5 * time.Second)
	h.sky.SetJob(cluster(id), "RUNNING", &start, nil)
	return upAt
}

// teardown steps until the backend has torn the cluster down.
func (h *e2e) teardown(id string) {
	h.t.Helper()
	for i := 0; i < 5 && h.sky.Exists(cluster(id)); i++ {
		h.step()
	}
	h.step()
	if h.sky.Exists(cluster(id)) {
		h.t.Fatalf("cluster of %s not torn down", id)
	}
	if _, err := h.svc.Backend.Status(id); err == nil {
		h.t.Fatalf("attempt of %s still tracked after teardown", id)
	}
}

func quotaOf(seconds int64) int64 {
	q, err := sku.SKU{PriceUSDPerHour: testRate}.EstimateQuota(seconds)
	if err != nil {
		panic(err)
	}
	return q
}

func usd(seconds int64) string { return quotaUSD(quotaOf(seconds)) }

// charged is what the console's consume log says the job cost.
func (h *e2e) charged(jobID string) int64 {
	var sum int64
	for _, l := range h.console.ConsumeLogs() {
		if l.JobID == jobID {
			sum += l.Quota
		}
	}
	return sum
}

// assertBilled checks the end state of every job that ran: the ledger, the
// console's consume log and the Job object all say exactly quota(seconds),
// and the hold is gone.
func (h *e2e) assertBilled(key, id string, seconds int64) {
	h.t.Helper()
	evs, err := h.svc.Ledger.Events(h.ctx, id)
	h.must(err)
	var qty int64
	for _, e := range evs {
		qty += e.Quantity
	}
	if qty != seconds {
		h.t.Errorf("ledger meters %d s, want %d", qty, seconds)
	}
	if got := h.charged(id); got != quotaOf(seconds) {
		h.t.Errorf("console charged %d, want quota(%d s) = %d", got, seconds, quotaOf(seconds))
	}
	hd, err := h.svc.Ledger.Hold(h.ctx, id)
	h.must(err)
	if hd.State != "closed" {
		h.t.Errorf("hold %s, want closed", hd.State)
	}
	if ch, _ := h.console.Hold(hd.HoldID); ch.Status == consoletest.HoldActive {
		h.t.Error("console hold still active")
	}
	j := h.get(key, id)
	if j.Usage.GPUSeconds != seconds || j.Usage.SettledCostUSD != usd(seconds) || j.Usage.HoldUSD != "0.0000" {
		h.t.Errorf("usage %+v, want %d s settled %s, hold released", j.Usage, seconds, usd(seconds))
	}
}

func templateBody(extra string) string {
	return `{"template":"batch-infer-vllm","spec":{"inputs":[{"source":"https://data.example.com/batches/requests.jsonl","mount":"/mnt/in"}]` + extra + `},"metadata":{"pipeline":"nightly"}}`
}

func plainBody(runtime int64) string {
	return fmt.Sprintf(`{"sku":"a10g-24gb-x1","spec":{"image":"ghcr.io/acme/embed:1.4","command":["python","run.py"],"max_runtime_seconds":%d}}`, runtime)
}

// --- scenarios -----------------------------------------------------------------

func TestEndToEnd(t *testing.T) {
	if os.Getenv("COMPUTE_TEST_DATABASE_URL") == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping the end-to-end test")
	}
	t.Run("template job runs, settles every 300 s, succeeds and is delivered", testTemplateJobLifecycle)
	t.Run("cancel mid-run bills to the cancel", testCancelMidRun)
	t.Run("interruption bills the seconds run", testInterruption)
	t.Run("console down mid-job loses no usage and stops at the hold", testConsoleDownMidJob)
	t.Run("insufficient balance persists nothing", testInsufficientBalance)
	t.Run("quota exceeded places no hold", testQuotaExceeded)
	t.Run("idempotent replay returns the same job", testIdempotentReplay)
	t.Run("owners are isolated", testOwnerIsolation)
	t.Run("outputs of a failed job are stored and served", testArtifactsOfFailedJob)
	t.Run("requests refused before anything is held", testRefusedRequests)
	t.Run("outputs refused without artifact storage", testOutputsWithoutStorage)
	t.Run("submit refused without a SkyPilot API server", testNoSkyPilot)

	doc := loadSpec(t)
	dbLess := dbLessErrorCodes(t, doc)
	e2eSeenMu.Lock()
	defer e2eSeenMu.Unlock()
	if stale := unproducedErrorCodes(t, doc, dbLess, e2eSeen); len(stale) > 0 && !t.Failed() {
		t.Errorf("Error.x-error-codes lists codes neither a scenario nor the database-less servers produced: %v", stale)
	}
}

func testTemplateJobLifecycle(t *testing.T) {
	h := newE2E(t)
	tok := int64(11)
	_, err := h.st.CreateWebhook(h.ctx, store.NewWebhook{Owner: alice, TokenID: &tok, URL: h.hookSv.URL + "/hooks",
		Secret: events.NewSecret(), EventTypes: events.TypeStrings(events.DefaultWebhookTypes)})
	h.must(err)

	r, j := h.submit(aliceKey, templateBody(""))
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	if j.Status != store.StatusProvisioning || j.SKU != "a10g-24gb-x1" || j.Placement != nil && string(j.Placement) != "null" {
		t.Fatalf("submitted job %+v", j)
	}
	// metering.md H1: a 3600 s job holds its whole ceiling, 1 h at 1.20.
	if j.Usage.HoldUSD != "1.2000" || j.Usage.EstimatedCostUSD != "1.2000" || j.Usage.GPUSeconds != 0 || j.Metadata["pipeline"] != "nightly" {
		t.Fatalf("usage at submit %+v", j)
	}
	hd, err := h.svc.Ledger.Hold(h.ctx, j.ID)
	h.must(err)
	if ch, ok := h.console.Hold(hd.HoldID); !ok || ch.Status != consoletest.HoldActive || ch.Quota != quotaOf(3600) || ch.JobID != j.ID {
		t.Fatalf("console hold %+v %v", ch, ok)
	}
	launches := h.sky.Launches()
	if len(launches) != 1 || launches[0].Cluster != cluster(j.ID) || !strings.Contains(launches[0].TaskRaw, "vllm run-batch") ||
		!strings.Contains(launches[0].TaskRaw, "data.example.com/batches/requests.jsonl") || !strings.Contains(launches[0].TaskRaw, artifacts.EnvToken) {
		t.Fatalf("launch %+v", launches)
	}

	upAt := h.up(j.ID)
	running := h.get(aliceKey, j.ID)
	var p store.Placement
	if err := json.Unmarshal(running.Placement, &p); err != nil || p != (store.Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"}) {
		t.Fatalf("placement %s (%v)", running.Placement, err)
	}
	if running.Status != store.StatusRunning || running.StartedAt == nil {
		t.Fatalf("running job %+v", running)
	}
	if pe := h.event(aliceKey, j.ID, "job.placed"); !strings.Contains(string(pe.Data), `"instance_type":"g5.xlarge"`) {
		t.Fatalf("job.placed %s", pe.Data)
	}

	h.sky.SetLogs(cluster(j.ID), "loading model\nprocessed 10 requests\n")
	if lr := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/logs", aliceKey, ""); lr.code != 200 || !strings.Contains(string(lr.body), "processed 10 requests") {
		t.Fatalf("logs: %d %q", lr.code, lr.body)
	}

	// R1: one settle per 300 s boundary from UP, cumulative.
	h.runFor(20*time.Minute+30*time.Second, 30*time.Second)
	var settled []int64
	for _, c := range h.console.CallsTo(consoletest.RouteSettle) {
		var b struct {
			CumulativeQuota int64 `json:"cumulative_quota"`
		}
		h.must(json.Unmarshal(c.Body, &b))
		settled = append(settled, b.CumulativeQuota)
	}
	if fmt.Sprint(settled) != fmt.Sprint([]int64{quotaOf(300), quotaOf(600), quotaOf(900), quotaOf(1200)}) {
		t.Fatalf("settles %v", settled)
	}
	if mid := h.get(aliceKey, j.ID); mid.Usage.SettledCostUSD != usd(1200) || mid.Usage.GPUSeconds < 1200 {
		t.Fatalf("usage mid-run %+v", mid.Usage)
	}

	end := upAt.Add(1234*time.Second + 500*time.Millisecond)
	h.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	h.clock.Advance(10 * time.Second)
	h.step()
	h.teardown(j.ID)

	done := h.get(aliceKey, j.ID)
	if done.Status != store.StatusSucceeded || done.FinishedAt == nil || done.Error != nil {
		t.Fatalf("finished job %+v", done)
	}
	h.assertBilled(aliceKey, j.ID, 1234)
	want := []string{"job.queued", "job.provisioning", "job.placed", "job.running", "job.succeeded"}
	if got := h.eventTypes(aliceKey, j.ID); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events %v, want %v", got, want)
	}

	// The terminal event reaches the webhook, signed, exactly as the events
	// endpoint shows it; the other types are not in its default set.
	if _, err := h.svc.Events.RunOnce(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	if len(h.hooks) != 1 {
		t.Fatalf("webhook received %d deliveries", len(h.hooks))
	}
	got := h.hooks[0]
	var obj events.Object
	h.must(json.Unmarshal(got.Body, &obj))
	if obj.Type != "job.succeeded" || obj.JobID != j.ID || got.Header.Get(events.HeaderEventID) != obj.ID {
		t.Fatalf("delivery %s %v", got.Body, got.Header)
	}
	whs, err := h.st.DeliveriesOf(h.ctx, obj.ID)
	h.must(err)
	if len(whs) != 1 || whs[0].Status != "delivered" {
		t.Fatalf("delivery state %+v", whs)
	}
	ts, _ := strconv.ParseInt(got.Header.Get(console.HeaderTimestamp), 10, 64)
	if got.Header.Get(console.HeaderSignature) == "" || ts == 0 {
		t.Fatal("delivery is not signed")
	}
}

func testCancelMidRun(t *testing.T) {
	h := newE2E(t)
	r, j := h.submit(aliceKey, plainBody(3600))
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	upAt := h.up(j.ID)
	h.runFor(7*time.Minute, time.Minute)
	h.clock.Advance(17 * time.Second)
	acceptedAt := h.clock.Now()
	cr := h.call(http.MethodPost, "/compute/v1/jobs/"+j.ID+"/cancel", aliceKey, "")
	if cr.code != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", cr.code, cr.body)
	}
	var cj jobView
	cr.json(t, &cj)
	if cj.Status != store.StatusCancelled {
		t.Fatalf("after cancel %+v", cj)
	}
	// Teardown takes a while; none of it is billed (B11).
	h.clock.Advance(3 * time.Minute)
	h.teardown(j.ID)
	h.assertBilled(aliceKey, j.ID, int64(acceptedAt.Sub(upAt)/time.Second))
	if e := h.event(aliceKey, j.ID, "job.cancelled"); !strings.Contains(string(e.Data), `"reason":"customer"`) {
		t.Fatalf("job.cancelled %s", e.Data)
	}
	if again := h.call(http.MethodPost, "/compute/v1/jobs/"+j.ID+"/cancel", aliceKey, ""); again.code != http.StatusConflict || again.errCode() != "job_terminal" {
		t.Fatalf("cancel of a cancelled job: %d %s", again.code, again.body)
	}
}

func testInterruption(t *testing.T) {
	h := newE2E(t)
	_, j := h.submit(aliceKey, plainBody(3600))
	h.up(j.ID)
	h.runFor(10*time.Minute, time.Minute)
	lastUp := h.clock.Now()
	h.sky.Vanish(cluster(j.ID))
	// Noticed 90 s later; that gap is ours (I3).
	h.clock.Advance(90 * time.Second)
	h.step()
	h.teardown(j.ID)
	got := h.get(aliceKey, j.ID)
	if got.Status != store.StatusInterrupted || got.Error == nil || got.Error.Code != backend.CodeProviderFault {
		t.Fatalf("interrupted job %+v", got)
	}
	upAt := lastUp.Add(-10 * time.Minute)
	h.assertBilled(aliceKey, j.ID, int64(lastUp.Sub(upAt)/time.Second))
	if e := h.event(aliceKey, j.ID, "job.preempted"); !strings.Contains(string(e.Data), `"provider":"aws"`) {
		t.Fatalf("job.preempted %s", e.Data)
	}
}

func testConsoleDownMidJob(t *testing.T) {
	h := newE2E(t)
	// Four hours: the hold covers two (H1), so an outage can exhaust it.
	r, j := h.submit(aliceKey, plainBody(4*3600))
	if r.code != http.StatusCreated || j.Usage.HoldUSD != "2.4000" {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	upAt := h.up(j.ID)
	h.runFor(10*time.Minute, 5*time.Minute)
	h.console.SetDown(true)
	for i := 0; i < 40; i++ {
		h.clock.Advance(5 * time.Minute)
		h.step()
		if st, _ := h.st.Get(h.ctx, alice, j.ID); st.Status.Terminal() {
			break
		}
	}
	stopped, err := h.st.Get(h.ctx, alice, j.ID)
	h.must(err)
	if stopped.Status != store.StatusCancelled || stopped.Error == nil || stopped.Error.Code != string(ledger.EndBillingUnavailable) {
		t.Fatalf("job after the outage ran on: %+v", stopped)
	}
	// The key cannot be checked either while the console is down.
	if r := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID, aliceKey, ""); r.code != http.StatusServiceUnavailable || r.errCode() != "console_unavailable" {
		t.Fatalf("read during the outage: %d %s", r.code, r.body)
	}
	h.teardown(j.ID)

	// H5: stopped once less than 10 min of the 2 h hold was left, at the
	// first tick past 6600 s.
	billed := int64(6900)
	evs, err := h.svc.Ledger.Events(h.ctx, j.ID)
	h.must(err)
	var qty int64
	for _, e := range evs {
		qty += e.Quantity
	}
	if qty != billed {
		t.Fatalf("ledger holds %d s through the outage, want %d (UP at %s)", qty, billed, upAt)
	}
	if got := h.charged(j.ID); got >= quotaOf(billed) {
		t.Fatalf("console charged %d during its own outage", got)
	}

	h.console.SetDown(false)
	h.clock.Advance(5 * time.Minute)
	h.step()
	h.assertBilled(aliceKey, j.ID, billed)
	if e := h.event(aliceKey, j.ID, "job.cancelled"); !strings.Contains(string(e.Data), `"reason":"billing_unavailable"`) {
		t.Fatalf("job.cancelled %s", e.Data)
	}
}

func testInsufficientBalance(t *testing.T) {
	h := newE2E(t)
	before := h.console.Balance(1003)
	r, _ := h.submit(carolKey, plainBody(3600))
	if r.code != http.StatusPaymentRequired || r.errCode() != "insufficient_balance" ||
		!strings.Contains(string(r.body), "USD 1.2000") {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	if b := h.console.Balance(1003); b != before {
		t.Fatalf("balance moved %d -> %d", before, b)
	}
	l := h.call(http.MethodGet, "/compute/v1/jobs", carolKey, "")
	var list struct{ Data []jobView }
	l.json(t, &list)
	if l.code != 200 || len(list.Data) != 0 {
		t.Fatalf("carol's jobs: %d %s", l.code, l.body)
	}
	live, err := h.st.ActiveJobs(h.ctx)
	h.must(err)
	if len(live) != 0 || len(h.sky.Launches()) != 0 {
		t.Fatalf("a refused hold persisted %d jobs and launched %d", len(live), len(h.sky.Launches()))
	}
	for _, c := range h.console.CallsTo(consoletest.RouteHold) {
		var hr struct {
			HoldID string `json:"hold_id"`
		}
		h.must(json.Unmarshal(c.Body, &hr))
		if ch, ok := h.console.Hold(hr.HoldID); ok && ch.Status == consoletest.HoldActive {
			t.Fatal("a refused submit left an active hold")
		}
	}
}

func testQuotaExceeded(t *testing.T) {
	h := newE2E(t)
	var ids []string
	for i := 0; i < 2; i++ {
		r, j := h.submit(aliceKey, plainBody(600))
		if r.code != http.StatusCreated {
			t.Fatalf("submit %d: %d %s", i, r.code, r.body)
		}
		ids = append(ids, j.ID)
	}
	holds := len(h.console.CallsTo(consoletest.RouteHold))
	r, _ := h.submit(aliceKey, templateBody(""))
	if r.code != http.StatusForbidden || r.errCode() != "quota_exceeded" || r.hdr.Get("Retry-After") != "" {
		t.Fatalf("third submit: %d %s", r.code, r.body)
	}
	var e struct {
		Error struct{ Quotas []quota.Violation } `json:"error"`
	}
	r.json(t, &e)
	if len(e.Error.Quotas) == 0 || e.Error.Quotas[0].Limit != quota.LimitActiveJobs || e.Error.Quotas[0].Headroom != 0 {
		t.Fatalf("violations %+v", e.Error.Quotas)
	}
	if n := len(h.console.CallsTo(consoletest.RouteHold)); n != holds {
		t.Fatalf("an over-quota submit called the console's hold (%d -> %d)", holds, n)
	}
	// A queued or provisioning job ends with its hold released in full
	// and no consume-log row (B10).
	for _, id := range ids {
		if c := h.call(http.MethodPost, "/compute/v1/jobs/"+id+"/cancel", aliceKey, ""); c.code != http.StatusAccepted {
			t.Fatalf("cancel: %d %s", c.code, c.body)
		}
		h.teardown(id)
		hd, err := h.svc.Ledger.Hold(h.ctx, id)
		h.must(err)
		if hd.State != "closed" || h.charged(id) != 0 {
			t.Fatalf("hold %+v charged %d", hd, h.charged(id))
		}
	}
	if r, _ := h.submit(aliceKey, plainBody(600)); r.code != http.StatusCreated {
		t.Fatalf("submit after the slots freed: %d %s", r.code, r.body)
	}
}

func testIdempotentReplay(t *testing.T) {
	h := newE2E(t)
	r1, j1 := h.submit(aliceKey, plainBody(600), "Idempotency-Key", "nightly-2026-10-03")
	r2, j2 := h.submit(aliceKey, plainBody(600), "Idempotency-Key", "nightly-2026-10-03")
	if r1.code != http.StatusCreated || r2.code != http.StatusOK || j1.ID != j2.ID {
		t.Fatalf("replay: %d %s / %d %s", r1.code, j1.ID, r2.code, j2.ID)
	}
	if n := len(h.console.CallsTo(consoletest.RouteHold)); n != 1 || len(h.sky.Launches()) != 1 {
		t.Fatalf("replay placed %d holds and %d launches", n, len(h.sky.Launches()))
	}
	r3, _ := h.submit(aliceKey, plainBody(601), "Idempotency-Key", "nightly-2026-10-03")
	if r3.code != http.StatusConflict || r3.errCode() != "idempotency_conflict" {
		t.Fatalf("same key, other body: %d %s", r3.code, r3.body)
	}
	// Keys are per owner: Bob's same key is his own first submit.
	if r4, j4 := h.submit(bobKey, plainBody(600), "Idempotency-Key", "nightly-2026-10-03"); r4.code != http.StatusCreated || j4.ID == j1.ID {
		t.Fatalf("other owner, same key: %d %s", r4.code, r4.body)
	}
	if r5, _ := h.submit(aliceKey, plainBody(600), "Idempotency-Key", strings.Repeat("k", 256)); r5.code != 400 || r5.errCode() != "invalid_idempotency_key" {
		t.Fatalf("long key: %d %s", r5.code, r5.body)
	}
}

func testOwnerIsolation(t *testing.T) {
	h := newE2E(t)
	_, j := h.submit(aliceKey, plainBody(600))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/compute/v1/jobs/" + j.ID},
		{http.MethodGet, "/compute/v1/jobs/" + j.ID + "/events"},
		{http.MethodGet, "/compute/v1/jobs/" + j.ID + "/logs"},
		{http.MethodGet, "/compute/v1/jobs/" + j.ID + "/artifacts"},
		{http.MethodPost, "/compute/v1/jobs/" + j.ID + "/cancel"},
	} {
		if r := h.call(c.method, c.path, bobKey, ""); r.code != http.StatusNotFound || r.errCode() != "job_not_found" {
			t.Errorf("bob %s %s: %d %s", c.method, c.path, r.code, r.body)
		}
	}
	l := h.call(http.MethodGet, "/compute/v1/jobs", bobKey, "")
	var list struct{ Data []jobView }
	l.json(t, &list)
	if l.code != 200 || len(list.Data) != 0 {
		t.Fatalf("bob's list: %s", l.body)
	}
	if got := h.get(aliceKey, j.ID); got.Status != store.StatusProvisioning {
		t.Fatalf("bob's cancel touched alice's job: %+v", got)
	}

	// Alice's list: newest first, cursor, status filter.
	_, j2 := h.submit(aliceKey, plainBody(600))
	r := h.call(http.MethodGet, "/compute/v1/jobs?limit=1", aliceKey, "")
	var page struct {
		Data    []jobView `json:"data"`
		HasMore bool      `json:"has_more"`
		Next    string    `json:"next"`
	}
	r.json(t, &page)
	if len(page.Data) != 1 || page.Data[0].ID != j2.ID || !page.HasMore || page.Next != j2.ID {
		t.Fatalf("page 1: %s", r.body)
	}
	r = h.call(http.MethodGet, "/compute/v1/jobs?limit=1&after="+page.Next, aliceKey, "")
	page.Data, page.HasMore = nil, false
	r.json(t, &page)
	if len(page.Data) != 1 || page.Data[0].ID != j.ID || page.HasMore {
		t.Fatalf("page 2: %s", r.body)
	}
	r = h.call(http.MethodGet, "/compute/v1/jobs?status=succeeded&status=failed", aliceKey, "")
	page.Data = nil
	r.json(t, &page)
	if len(page.Data) != 0 {
		t.Fatalf("status filter: %s", r.body)
	}
	for _, c := range []struct{ path, code string }{
		{"/compute/v1/jobs?status=done", "invalid_status"},
		{"/compute/v1/jobs?limit=0", "invalid_limit"},
		{"/compute/v1/jobs?after=" + j.ID + "x", "invalid_cursor"},
		{"/compute/v1/jobs/" + j.ID + "/events?after=evt_00000000000000000000000000", "invalid_cursor"},
		{"/compute/v1/jobs/" + j.ID + "/events?limit=0", "invalid_limit"},
	} {
		if r := h.call(http.MethodGet, c.path, aliceKey, ""); r.code != 400 || r.errCode() != c.code {
			t.Errorf("%s: %d %s", c.path, r.code, r.body)
		}
	}
	// Bob cannot page through Alice's jobs with her ids either.
	if r := h.call(http.MethodGet, "/compute/v1/jobs?after="+j.ID, bobKey, ""); r.code != 400 || r.errCode() != "invalid_cursor" {
		t.Errorf("bob with alice's cursor: %d %s", r.code, r.body)
	}
}

func testArtifactsOfFailedJob(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "sha256sum", "find"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	h := newE2E(t)
	mount := filepath.Join(t.TempDir(), "out")
	cmd := fmt.Sprintf(`printf 'line 1\n' > %[1]s/results.jsonl && mkdir -p %[1]s/logs && printf 'summary\n' > %[1]s/logs/summary.txt && echo working && exit 3`, mount)
	body := fmt.Sprintf(`{"sku":"a10g-24gb-x1","spec":{"image":"ghcr.io/acme/embed:1.4","command":["bash","-c",%q],"outputs":[{"mount":%q}],"max_runtime_seconds":3600}}`, cmd, mount)
	r, j := h.submit(aliceKey, body)
	if r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	_, other := h.submit(aliceKey, plainBody(600))
	upAt := h.up(j.ID)

	var task struct {
		Run  string            `json:"run"`
		Envs map[string]string `json:"envs"`
	}
	for _, l := range h.sky.Launches() {
		if l.Cluster == cluster(j.ID) {
			h.must(json.Unmarshal([]byte(l.TaskRaw), &task))
		}
	}
	if task.Envs[artifacts.EnvToken] == "" {
		t.Fatalf("task has no upload token: %+v", task)
	}

	// The node's own upload step, run for real against compute-api and S3.
	c := exec.Command("bash", "-c", task.Run)
	c.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range task.Envs {
		c.Env = append(c.Env, k+"="+v)
	}
	out, err := c.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 || !strings.Contains(string(out), "2 output file(s) processed") {
		t.Fatalf("run step: %v\n%s", err, out)
	}

	// A PUT URL is good only for the key it was signed for: moved to the
	// other job's prefix, S3 refuses it.
	form := url.Values{"path": {mount + "/extra.bin"}, "size": {"1"}, "sha256": {"6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d"}}
	req, _ := http.NewRequest(http.MethodPost, task.Envs[artifacts.EnvURL]+"/uploads", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+task.Envs[artifacts.EnvToken])
	presign, err := http.DefaultClient.Do(req)
	h.must(err)
	line, _ := io.ReadAll(presign.Body)
	presign.Body.Close()
	fields := strings.Fields(string(line))
	if presign.StatusCode != 200 || len(fields) != 2 {
		t.Fatalf("presign: %d %s", presign.StatusCode, line)
	}
	moved := strings.Replace(fields[1], "jobs/"+j.ID+"/", "jobs/"+other.ID+"/", 1)
	if moved == fields[1] {
		t.Fatalf("presigned URL does not name the job prefix: %s", fields[1])
	}
	put, _ := http.NewRequest(http.MethodPut, moved, bytes.NewReader([]byte{1}))
	put.Header.Set("x-amz-checksum-sha256", fields[0])
	pr, err := h.s3.Server.Client().Do(put)
	h.must(err)
	pr.Body.Close()
	if pr.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT moved to another job's key: %d", pr.StatusCode)
	}
	for _, k := range h.s3.Keys() {
		if strings.HasPrefix(k, "jobs/"+other.ID+"/") {
			t.Fatalf("object written under the other job: %s", k)
		}
	}

	end := upAt.Add(42 * time.Second)
	h.sky.SetJob(cluster(j.ID), "FAILED", nil, &end)
	h.clock.Advance(10 * time.Second)
	h.step()
	h.teardown(j.ID)
	failed := h.get(aliceKey, j.ID)
	if failed.Status != store.StatusFailed || failed.Error == nil || failed.Error.Code != backend.CodeNonZeroExit {
		t.Fatalf("job %+v", failed)
	}
	if e := h.event(aliceKey, j.ID, "job.failed"); !strings.Contains(string(e.Data), `"retryable":false`) {
		t.Fatalf("job.failed %s", e.Data)
	}
	h.assertBilled(aliceKey, j.ID, 42)

	ar := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/artifacts", aliceKey, "")
	var l struct {
		Data     []artifactView `json:"data"`
		Complete bool           `json:"complete"`
	}
	ar.json(t, &l)
	want := map[string]string{mount + "/results.jsonl": "line 1\n", mount + "/logs/summary.txt": "summary\n"}
	if ar.code != 200 || !l.Complete || len(l.Data) != len(want) {
		t.Fatalf("artifacts: %d %s", ar.code, ar.body)
	}
	for _, a := range l.Data {
		resp, err := h.s3.Server.Client().Get(a.URL)
		h.must(err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != want[a.Path] {
			t.Errorf("%s: %d %q", a.Path, resp.StatusCode, b)
		}
	}
	if r := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/artifacts", bobKey, ""); r.code != http.StatusNotFound {
		t.Fatalf("bob lists alice's artifacts: %d %s", r.code, r.body)
	}
}

// Every request below is refused before the console is asked for a hold.
func testRefusedRequests(t *testing.T) {
	h := newE2E(t)
	for _, c := range []struct {
		name, key, body string
		status          int
		code            string
	}{
		{"unknown key", "sk-nobody-000000000", plainBody(600), 401, "invalid_key"},
		{"disabled user", daveKey, plainBody(600), 403, "account_disabled"},
		{"suspended tenant", erinKey, plainBody(600), 403, "account_suspended"},
		{"no image", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"command":["true"],"max_runtime_seconds":60}}`, 400, "missing_image"},
		{"no command", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"image":"x","max_runtime_seconds":60}}`, 400, "missing_command"},
		{"s3 input", aliceKey, `{"template":"batch-infer-vllm","spec":{"inputs":[{"source":"s3://acme/in/","mount":"/mnt/in"}]}}`, 400, "invalid_input_source"},
		{"reserved env", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"image":"x","command":["true"],"env":{"UNIFY_COMPUTE_X":"1"},"max_runtime_seconds":60}}`, 400, "reserved_env"},
		{"bad env", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"image":"x","command":["true"],"env":{"1X":"1"},"max_runtime_seconds":60}}`, 400, "invalid_env"},
		{"relative mount", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"image":"x","command":["true"],"outputs":[{"mount":"out"}],"max_runtime_seconds":60}}`, 400, "invalid_mount"},
		{"too much metadata", aliceKey, `{"sku":"a10g-24gb-x1","spec":{"image":"x","command":["true"],"max_runtime_seconds":60},"metadata":{` + metadataPairs(17) + `}}`, 400, "invalid_metadata"},
		{"unknown template", aliceKey, `{"template":"nope"}`, 400, "unknown_template"},
		// template_unavailable: every shipped template is available now; the
		// code is produced by templates_test.go against its own template set.
		{"field not overridable", aliceKey, `{"template":"batch-infer-vllm","spec":{"image":"x"}}`, 400, "field_not_overridable"},
		{"missing input", aliceKey, `{"template":"batch-infer-vllm"}`, 400, "missing_input"},
	} {
		if r, _ := h.submit(c.key, c.body); r.code != c.status || r.errCode() != c.code {
			t.Errorf("%s: %d %s, want %d %s", c.name, r.code, r.body, c.status, c.code)
		}
	}
	if n := len(h.console.CallsTo(consoletest.RouteHold)); n != 0 {
		t.Fatalf("refused requests placed %d holds", n)
	}
	if r := h.call(http.MethodGet, "/compute/v1/jobs", "sk-nobody-000000000", ""); r.code != 401 {
		t.Fatalf("unknown key on a read: %d", r.code)
	}

	// Logs that cannot be read from the cluster are a 503, not an empty 200.
	_, j := h.submit(aliceKey, plainBody(600))
	h.up(j.ID)
	h.sky.FailNext("/logs", http.StatusBadGateway)
	if r := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/logs", aliceKey, ""); r.code != 503 || r.errCode() != "logs_unavailable" {
		t.Fatalf("logs with the API server failing: %d %s", r.code, r.body)
	}
	if r := h.call(http.MethodGet, "/compute/v1/jobs/"+j.ID+"/logs?follow=true", aliceKey, ""); r.code != 501 {
		t.Fatalf("follow: %d", r.code)
	}

	// The console refusing our own signature is ours to fix, not the
	// customer's key: 503, never 401.
	h.console.InjectFault(consoletest.Fault{Route: consoletest.RouteVerify, Status: http.StatusUnauthorized, Times: 1})
	if r := h.call(http.MethodGet, "/compute/v1/jobs", "sk-fresh-key-0000099", ""); r.code != 503 || r.errCode() != "console_unavailable" {
		t.Fatalf("bridge refusing our signature: %d %s", r.code, r.body)
	}
}

func metadataPairs(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":"v"`, i)
	}
	return b.String()
}

func testOutputsWithoutStorage(t *testing.T) {
	h := newE2E(t, e2eOptions{noArtifacts: true})
	r, _ := h.submit(aliceKey, templateBody(""))
	if r.code != http.StatusNotImplemented || r.errCode() != "outputs_unsupported" {
		t.Fatalf("template with outputs and no storage: %d %s", r.code, r.body)
	}
	if n := len(h.console.CallsTo(consoletest.RouteHold)); n != 0 {
		t.Fatalf("refused submit placed %d holds", n)
	}
	// A job without outputs still runs.
	if r, _ := h.submit(aliceKey, plainBody(600)); r.code != http.StatusCreated {
		t.Fatalf("plain submit: %d %s", r.code, r.body)
	}
}

func testNoSkyPilot(t *testing.T) {
	h := newE2E(t, e2eOptions{noSkyPilot: true})
	r, _ := h.submit(aliceKey, plainBody(600))
	if r.code != http.StatusServiceUnavailable || r.errCode() != "upstream_unavailable" {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	if n := len(h.console.CallsTo(consoletest.RouteHold)); n != 0 {
		t.Fatalf("refused submit placed %d holds", n)
	}
	if r := h.call(http.MethodGet, "/compute/v1/jobs", aliceKey, ""); r.code != 200 {
		t.Fatalf("reads still work: %d %s", r.code, r.body)
	}
}
