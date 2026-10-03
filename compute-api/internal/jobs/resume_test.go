package jobs_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/events"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

// These tests restart compute-api around a job: one jobs.Service drives it,
// is dropped without any shutdown (as a killed process is), and a new
// Service built on the same Postgres store, fake console, fake SkyPilot and
// fake clock takes over. Nothing carries over between the two except what is
// durable: the database and the fakes' own state.

const (
	secret   = "0123456789abcdef0123456789abcdef"
	owner    = "user:1001"
	userID   = 1001
	tokenID  = 11
	skuID    = "a10g-24gb-x1"
	rate     = "1.2000"
	upRes    = "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)"
	skyToken = "sky_resume_token"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type world struct {
	t       *testing.T
	ctx     context.Context
	clock   *clock
	st      *store.Store
	console *consoletest.Server
	sky     *skypilottest.Server
	cat     *sku.Catalogue
	item    sku.SKU

	hookMu sync.Mutex
	hooks  []events.Object
	hookSv *httptest.Server
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st := storetest.New(t)
	// Ahead of the wall clock, as in the API's end-to-end tests: rows the
	// database stamps with now() must never be in the fake clock's future.
	c := &clock{t: time.Now().Add(time.Minute).Truncate(time.Second)}
	cat, err := sku.Load(filepath.Join("..", "..", "config", "skus.json"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := cat.Get(skuID)
	if err != nil {
		t.Fatal(err)
	}
	fc := consoletest.New([]byte(secret))
	t.Cleanup(fc.Close)
	fc.SetNow(c.Now)
	if billingtest.Parity(t) {
		book, local := billingtest.Book(t, st.Pool(), c.Now)
		fc.UseBook(book)
		t.Cleanup(func() { billingtest.AssertInvariant(t, local) })
	}
	fc.SeedUser(consoletest.User{ID: userID, Quota: 1_000_000_000})
	fc.SeedKey(consoletest.Key{Key: "sk-resume-0000000001", TokenID: tokenID, UserID: userID, Unlimited: true})

	sky := skypilottest.New()
	sky.Token = skyToken
	t.Cleanup(sky.Close)
	sky.SetClock(c.Now)

	w := &world{t: t, ctx: context.Background(), clock: c, st: st, console: fc, sky: sky, cat: cat, item: item}
	w.hookSv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var o events.Object
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &o)
		w.hookMu.Lock()
		w.hooks = append(w.hooks, o)
		w.hookMu.Unlock()
	}))
	t.Cleanup(w.hookSv.Close)
	tok := int64(tokenID)
	if _, err := st.CreateWebhook(w.ctx, store.NewWebhook{Owner: owner, TokenID: &tok, URL: w.hookSv.URL + "/hooks",
		Secret: events.NewSecret(), EventTypes: events.TypeStrings(events.DefaultWebhookTypes)}); err != nil {
		t.Fatal(err)
	}
	return w
}

// process is one compute-api process: everything built fresh, nothing shared
// with an earlier one but the database and the fakes.
func (w *world) process(elect bool) *jobs.Service {
	w.t.Helper()
	return jobs.New(jobs.Config{
		Store: w.st, Console: w.console.Client(), Catalogue: w.cat, Elect: elect, Now: w.clock.Now,
		SkyPilot: &backend.Client{BaseURL: w.sky.URL, Token: skyToken, UserHash: "unifycmp"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) submit(svc *jobs.Service, maxRuntime int) store.Job {
	w.t.Helper()
	j, created, err := svc.Submit(w.ctx, jobs.Submission{
		Owner: owner, UserID: userID, TokenID: tokenID, SKU: w.item, Candidates: w.item.Providers,
		Spec: backend.Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: maxRuntime},
	})
	w.must(err)
	if !created {
		w.t.Fatal("submit replayed")
	}
	return j
}

func (w *world) job(id string) store.Job {
	w.t.Helper()
	j, err := w.st.Get(w.ctx, owner, id)
	w.must(err)
	return j
}

// step is one backend round and one ledger tick of svc.
func (w *world) step(svc *jobs.Service) {
	w.t.Helper()
	svc.Step(w.ctx)
	if err := svc.Ledger.Tick(w.ctx); err != nil {
		w.t.Logf("ledger tick: %v", err)
	}
}

func (w *world) runFor(svc *jobs.Service, d, every time.Duration) {
	w.t.Helper()
	for end := w.clock.Now().Add(d); w.clock.Now().Before(end); {
		w.clock.Advance(every)
		w.step(svc)
	}
}

// restart is a new process taking over at boot: reconcile, then one tick.
func (w *world) restart() *jobs.Service {
	w.t.Helper()
	svc := w.process(false)
	if err := svc.Reconcile(w.ctx); err != nil {
		w.t.Fatalf("reconcile: %v", err)
	}
	if err := svc.Ledger.Tick(w.ctx); err != nil {
		w.t.Logf("ledger tick: %v", err)
	}
	return svc
}

func cluster(id string) string { return backend.ClusterName(id, 1) }

// up provisions the job's cluster and has svc observe it UP, with the job
// submitted and running on the node. It returns where billing starts.
func (w *world) up(svc *jobs.Service, id string) time.Time {
	w.t.Helper()
	w.sky.Provision(cluster(id))
	w.step(svc)
	w.clock.Advance(time.Minute)
	w.sky.Up(cluster(id), "AWS", "us-east-1", upRes)
	upAt := w.clock.Now()
	w.step(svc)
	w.sky.SubmitJob(cluster(id))
	start := upAt.Add(5 * time.Second)
	w.sky.SetJob(cluster(id), "RUNNING", &start, nil)
	if j := w.job(id); j.Status != store.StatusRunning {
		w.t.Fatalf("job %s after UP: %s", id, j.Status)
	}
	return upAt
}

func (w *world) teardown(svc *jobs.Service, id string) {
	w.t.Helper()
	for i := 0; i < 5 && w.sky.Exists(cluster(id)); i++ {
		w.step(svc)
	}
	w.step(svc)
	if w.sky.Exists(cluster(id)) {
		w.t.Fatalf("cluster of %s not torn down", id)
	}
	if svc.Backend.Tracked(id) {
		w.t.Fatalf("attempt of %s still tracked after teardown", id)
	}
}

func quotaOf(seconds int64) int64 {
	q, err := sku.SKU{PriceUSDPerHour: rate}.EstimateQuota(seconds)
	if err != nil {
		panic(err)
	}
	return q
}

// assertBilledOnce checks that the job was billed exactly seconds, once: the
// ledger's intervals tile the attempt without overlap or gap, every console
// consume-log row has its own event id, they sum to quota(seconds), and the
// hold is closed on both sides.
func (w *world) assertBilledOnce(svc *jobs.Service, id string, upAt time.Time, seconds int64) {
	w.t.Helper()
	evs, err := svc.Ledger.Events(w.ctx, id)
	w.must(err)
	var qty int64
	cursor := upAt.Truncate(time.Millisecond)
	for _, e := range evs {
		qty += e.Quantity
		if e.Final {
			continue
		}
		if !e.From.Equal(cursor) {
			w.t.Errorf("usage event %s runs from %s, want %s (gap or overlap)", e.ID, e.From, cursor)
		}
		cursor = e.To
	}
	if qty != seconds {
		w.t.Errorf("ledger meters %d s, want %d", qty, seconds)
	}
	var charged int64
	seen := map[string]bool{}
	for _, l := range w.console.ConsumeLogs() {
		if l.JobID != id {
			continue
		}
		if seen[l.EventID] {
			w.t.Errorf("console charged event %s twice", l.EventID)
		}
		seen[l.EventID] = true
		charged += l.Quota
	}
	if charged != quotaOf(seconds) {
		w.t.Errorf("console charged %d, want quota(%d s) = %d", charged, seconds, quotaOf(seconds))
	}
	hd, err := svc.Ledger.Hold(w.ctx, id)
	w.must(err)
	if hd.State != "closed" {
		w.t.Errorf("hold %s, want closed", hd.State)
	}
	if ch, _ := w.console.Hold(hd.HoldID); ch.Status == consoletest.HoldActive {
		w.t.Error("console hold still active")
	}
}

func (w *world) eventTypes(id string) []string {
	w.t.Helper()
	page, err := w.st.ListEvents(w.ctx, owner, id, "", 100)
	w.must(err)
	var out []string
	for _, e := range page.Events {
		out = append(out, e.Type)
	}
	return out
}

// delivered runs the webhook worker and returns what reached the receiver.
func (w *world) delivered(svc *jobs.Service) []events.Object {
	w.t.Helper()
	if _, err := svc.Events.RunOnce(w.ctx); err != nil {
		w.t.Fatal(err)
	}
	w.hookMu.Lock()
	defer w.hookMu.Unlock()
	return append([]events.Object(nil), w.hooks...)
}

func (w *world) launches(id string) int {
	n := 0
	for _, l := range w.sky.Launches() {
		if l.Cluster == cluster(id) {
			n++
		}
	}
	return n
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// --- scenarios ---------------------------------------------------------------

// A release restarts compute-api in the middle of a run. The cluster stays UP
// the whole time, so the downtime is the customer's GPU time and is billed,
// continuing from the last settled interval: no gap, nothing twice.
func TestRestartMidRunResumesAndBillsOnce(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 7*time.Minute, time.Minute)

	// The process dies. Four minutes pass with nobody watching.
	w.clock.Advance(4 * time.Minute)
	svc := w.restart()

	snap, err := svc.Backend.Status(j.ID)
	w.must(err)
	if snap.Phase != "up" || snap.UpAt == nil || !snap.UpAt.Equal(upAt) || snap.Cluster != cluster(j.ID) {
		t.Fatalf("resumed attempt %+v, want up since %s", snap, upAt)
	}
	if snap.Placement == nil || snap.Placement.Cloud != "aws" || snap.Placement.InstanceType != "g5.xlarge" {
		t.Fatalf("resumed placement %+v", snap.Placement)
	}
	if got := w.job(j.ID).Status; got != store.StatusRunning {
		t.Fatalf("status after resume %s", got)
	}
	// The ledger has caught up to the first observation after the restart,
	// and the 300 s structure is intact across it.
	u, err := svc.Ledger.Usage(w.ctx, j.ID)
	w.must(err)
	if u.GPUSeconds != 600 {
		t.Fatalf("metered %d s after resume at +11 min, want the two boundaries at 300 and 600", u.GPUSeconds)
	}

	w.runFor(svc, 9*time.Minute, time.Minute)
	end := upAt.Add(20*time.Minute + 500*time.Millisecond)
	w.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	w.clock.Advance(10 * time.Second)
	w.step(svc)
	w.teardown(svc, j.ID)

	if got := w.job(j.ID); got.Status != store.StatusSucceeded || got.Error != nil {
		t.Fatalf("finished job %+v", got)
	}
	w.assertBilledOnce(svc, j.ID, upAt, 1200)
	want := []string{"job.queued", "job.provisioning", "job.placed", "job.running", "job.succeeded"}
	if got := w.eventTypes(j.ID); !eq(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if n := w.launches(j.ID); n != 1 {
		t.Fatalf("%d launches, want the one before the restart", n)
	}
	if hooks := w.delivered(svc); len(hooks) != 1 || hooks[0].Type != "job.succeeded" {
		t.Fatalf("webhooks %+v", hooks)
	}
}

// The job ends while compute-api is down. The new process learns the outcome
// from SkyPilot's queue, ends each attempt at the node's process exit, settles
// finally, emits the one terminal event and tears the cluster down.
func TestRestartFinalizesJobsThatEndedDuringDowntime(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	ok := w.submit(old, 3600)
	bad := w.submit(old, 3600)
	upOK := w.up(old, ok.ID)
	upBad := w.up(old, bad.ID)
	w.runFor(old, 6*time.Minute, time.Minute)

	w.clock.Advance(3 * time.Minute)
	endOK := upOK.Add(8*time.Minute + 250*time.Millisecond)
	endBad := upBad.Add(9 * time.Minute)
	w.sky.SetJob(cluster(ok.ID), "SUCCEEDED", nil, &endOK)
	w.sky.SetJob(cluster(bad.ID), "FAILED", nil, &endBad)
	w.clock.Advance(10 * time.Minute)

	svc := w.restart()
	if got := w.job(ok.ID); got.Status != store.StatusSucceeded {
		t.Fatalf("job that exited 0 reads %s", got.Status)
	}
	if got := w.job(bad.ID); got.Status != store.StatusFailed || got.Error == nil || got.Error.Code != backend.CodeNonZeroExit {
		t.Fatalf("job that exited non-zero reads %s %+v", got.Status, got.Error)
	}
	w.teardown(svc, ok.ID)
	w.teardown(svc, bad.ID)
	w.assertBilledOnce(svc, ok.ID, upOK, 480)
	w.assertBilledOnce(svc, bad.ID, upBad, 540)
	if got := w.eventTypes(ok.ID); !eq(got, []string{"job.queued", "job.provisioning", "job.placed", "job.running", "job.succeeded"}) {
		t.Fatalf("events %v", got)
	}
	if got := w.eventTypes(bad.ID); !eq(got, []string{"job.queued", "job.provisioning", "job.placed", "job.running", "job.failed"}) {
		t.Fatalf("events %v", got)
	}
	var types []string
	for _, h := range w.delivered(svc) {
		types = append(types, h.Type)
	}
	sort.Strings(types)
	if !eq(types, []string{"job.failed", "job.succeeded"}) {
		t.Fatalf("webhooks %v, want one terminal delivery per job", types)
	}

	// A second restart finds nothing live and changes nothing.
	before := len(w.console.ConsumeLogs())
	again := w.restart()
	w.step(again)
	if n := len(w.console.ConsumeLogs()); n != before {
		t.Fatalf("second restart charged again: %d -> %d consume-log rows", before, n)
	}
	if hooks := w.delivered(again); len(hooks) != 2 {
		t.Fatalf("second restart delivered again: %d webhooks", len(hooks))
	}
}

// The cluster disappears while compute-api is down. Nobody saw it go, so the
// attempt is billed to the last observation of UP (I3), never to when the new
// process noticed, and the hold's remainder is released.
func TestRestartWithVanishedClusterInterruptsAtLastUp(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 6*time.Minute, time.Minute)
	lastUp := w.clock.Now()

	w.clock.Advance(30 * time.Second)
	w.sky.Vanish(cluster(j.ID))
	w.clock.Advance(20 * time.Minute)
	svc := w.restart()
	w.teardown(svc, j.ID)

	got := w.job(j.ID)
	if got.Status != store.StatusInterrupted || got.Error == nil || got.Error.Code != backend.CodeProviderFault {
		t.Fatalf("job %s %+v, want interrupted/provider_fault", got.Status, got.Error)
	}
	w.assertBilledOnce(svc, j.ID, upAt, int64(lastUp.Sub(upAt)/time.Second))
	page, err := w.st.ListEvents(w.ctx, owner, j.ID, "", 100)
	w.must(err)
	last := page.Events[len(page.Events)-1]
	if last.Type != "job.preempted" || field(t, last.Data, "provider") != "aws" {
		t.Fatalf("terminal event %s %s", last.Type, last.Data)
	}
	if hooks := w.delivered(svc); len(hooks) != 1 || hooks[0].Type != "job.preempted" {
		t.Fatalf("webhooks %+v", hooks)
	}
}

// A cluster under the same name that SkyPilot launched after the attempt was
// seen UP is not the attempt's: the attempt did not stay UP while nobody
// watched, so it is interrupted at the last UP, and the newcomer torn down.
func TestRestartRejectsClusterRelaunchedUnderTheSameName(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 5*time.Minute, time.Minute)
	lastUp := w.clock.Now()

	w.clock.Advance(10 * time.Minute)
	w.sky.Up(cluster(j.ID), "AWS", "us-east-1", upRes)
	svc := w.restart()
	w.teardown(svc, j.ID)
	if got := w.job(j.ID); got.Status != store.StatusInterrupted {
		t.Fatalf("job %s, want interrupted", got.Status)
	}
	w.assertBilledOnce(svc, j.ID, upAt, int64(lastUp.Sub(upAt)/time.Second))
}

// A job that was persisted but never handed to SkyPilot (here: submitted on a
// process without the worker lock) is launched by the next process that
// drives jobs, once, and runs normally.
func TestRestartLaunchesQueuedJob(t *testing.T) {
	w := newWorld(t)
	reader := w.process(true)
	j := w.submit(reader, 3600)
	if j.Status != store.StatusQueued || len(w.sky.Launches()) != 0 {
		t.Fatalf("submitted job %s with %d launches, want queued and none", j.Status, len(w.sky.Launches()))
	}

	w.clock.Advance(2 * time.Minute)
	svc := w.restart()
	if got := w.job(j.ID).Status; got != store.StatusProvisioning || w.launches(j.ID) != 1 {
		t.Fatalf("after restart %s with %d launches", got, w.launches(j.ID))
	}
	// Reconciling again does not launch it twice.
	w.must(svc.Reconcile(w.ctx))
	if n := w.launches(j.ID); n != 1 {
		t.Fatalf("%d launches after a second reconcile", n)
	}
	upAt := w.up(svc, j.ID)
	w.runFor(svc, 3*time.Minute, time.Minute)
	end := upAt.Add(3 * time.Minute)
	w.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	w.step(svc)
	w.teardown(svc, j.ID)
	if got := w.job(j.ID).Status; got != store.StatusSucceeded {
		t.Fatalf("job %s", got)
	}
	w.assertBilledOnce(svc, j.ID, upAt, 180)
}

// A queued job's hold lasts max_runtime_seconds + 48 h from submit (H6), of
// which the last 24 h are kept for settlement. A job resumed later than a day
// after submit gets only the runtime its hold still covers; with less than
// MinLaunchRuntime of that left it is not started at all.
func TestRestartBoundsLateLaunchesByTheHold(t *testing.T) {
	w := newWorld(t)
	reader := w.process(true)
	short := w.submit(reader, 900)
	gone := w.submit(reader, 600)

	// Resumed 24 h + 301 s after submit. short: 900 + 48 h − 24 h − (24 h +
	// 301 s) = 599 s left, launched with that cap. gone: 600 − 301 = 299 s,
	// under min(MinLaunchRuntime, 600) = 300.
	w.clock.Advance(24*time.Hour + 301*time.Second)
	svc := w.restart()

	if got := w.job(gone.ID); got.Status != store.StatusCancelled || got.Error == nil || got.Error.Code != jobs.CodeQueueExpired {
		t.Fatalf("late job %s %+v, want cancelled/queue_expired", got.Status, got.Error)
	}
	if w.launches(gone.ID) != 0 {
		t.Fatal("expired job was launched")
	}
	hd, err := svc.Ledger.Hold(w.ctx, gone.ID)
	w.must(err)
	if hd.State != "closed" {
		t.Fatalf("expired job's hold %s", hd.State)
	}
	if ch, _ := w.console.Hold(hd.HoldID); ch.Status != consoletest.HoldReleased || ch.Settled != 0 {
		t.Fatalf("console hold %+v, want released with nothing settled", ch)
	}
	page, err := w.st.ListEvents(w.ctx, owner, gone.ID, "", 100)
	w.must(err)
	if last := page.Events[len(page.Events)-1]; last.Type != "job.cancelled" || field(t, last.Data, "reason") != jobs.CodeQueueExpired {
		t.Fatalf("terminal event %s %s", last.Type, last.Data)
	}

	if w.launches(short.ID) != 1 {
		t.Fatal("job with runtime left was not launched")
	}
	upAt := w.up(svc, short.ID)
	w.runFor(svc, 11*time.Minute, time.Minute)
	w.teardown(svc, short.ID)
	got := w.job(short.ID)
	if got.Status != store.StatusFailed || got.Error == nil || got.Error.Code != backend.CodeMaxRuntimeExceeded ||
		!strings.Contains(got.Error.Message, "599 s its hold still covered") {
		t.Fatalf("late job %s %+v", got.Status, got.Error)
	}
	w.assertBilledOnce(svc, short.ID, upAt, 599)
}

// A downtime longer than the hold's runway: the restarted process extends
// the hold for what accrued meanwhile before reporting UP again. The extend
// is answered by the console but the answer is lost, and the process dies
// again; the next one re-sends the same extend_id, and the console reserves
// once.
func TestRestartCatchesTheHoldUpAndReplaysTheExtendOnce(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 4*3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 10*time.Minute, 5*time.Minute)
	hd, err := old.Ledger.Hold(w.ctx, j.ID)
	w.must(err)
	if hd.CurrentQuota != quotaOf(7200) {
		t.Fatalf("hold before the downtime %d, want H1's 2 h", hd.CurrentQuota)
	}

	// 90 minutes down: 100 minutes accrued against a 120-minute hold.
	w.clock.Advance(90 * time.Minute)
	w.console.InjectFault(consoletest.Fault{Route: consoletest.RouteExtend, AfterApply: true})
	mid := w.process(false)
	w.must(mid.Reconcile(w.ctx))
	extends := w.console.CallsTo(consoletest.RouteExtend)
	if len(extends) != 1 {
		t.Fatalf("%d extends at the first restart", len(extends))
	}
	var first struct {
		ExtendID        string `json:"extend_id"`
		AdditionalQuota int64  `json:"additional_quota"`
	}
	w.must(json.Unmarshal(extends[0].Body, &first))
	ch, _ := w.console.Hold(hd.HoldID)
	if ch.Quota != quotaOf(7200)+first.AdditionalQuota {
		t.Fatalf("console hold %d after the lost extend, want %d", ch.Quota, quotaOf(7200)+first.AdditionalQuota)
	}

	// The process dies before its next tick; the next one boots at once.
	svc := w.restart()
	extends = w.console.CallsTo(consoletest.RouteExtend)
	var second struct {
		ExtendID        string `json:"extend_id"`
		AdditionalQuota int64  `json:"additional_quota"`
	}
	w.must(json.Unmarshal(extends[1].Body, &second))
	if second != first {
		t.Fatalf("re-sent extend %+v, want the pending %+v", second, first)
	}
	ch, _ = w.console.Hold(hd.HoldID)
	hd, err = svc.Ledger.Hold(w.ctx, j.ID)
	w.must(err)
	if ch.Quota != quotaOf(7200)+first.AdditionalQuota || hd.CurrentQuota != ch.Quota || hd.PendingExtendID != "" {
		t.Fatalf("console hold %d, ledger %+v; want one extend of %d", ch.Quota, hd, first.AdditionalQuota)
	}
	if got := w.job(j.ID).Status; got != store.StatusRunning {
		t.Fatalf("job %s after the catch-up, want still running", got)
	}

	w.runFor(svc, 10*time.Minute, 5*time.Minute)
	end := upAt.Add(110 * time.Minute)
	w.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	w.step(svc)
	w.teardown(svc, j.ID)
	if got := w.job(j.ID).Status; got != store.StatusSucceeded {
		t.Fatalf("job %s", got)
	}
	w.assertBilledOnce(svc, j.ID, upAt, 110*60)
}

// The final settle is applied by the console but its answer is lost, and the
// process dies. The next one re-sends the same event id; the console treats
// it as a replay, so the job is charged once.
func TestRestartReplaysALostFinalSettleOnce(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 4*time.Minute, time.Minute)
	end := upAt.Add(4*time.Minute + 10*time.Second)
	w.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	w.console.InjectFault(consoletest.Fault{Route: consoletest.RouteSettle, AfterApply: true})
	w.clock.Advance(15 * time.Second)
	old.Step(w.ctx)
	if got := w.job(j.ID).Status; got != store.StatusSucceeded {
		t.Fatalf("job %s", got)
	}
	settles := w.console.CallsTo(consoletest.RouteSettle)
	if len(settles) != 1 {
		t.Fatalf("%d settles before the restart", len(settles))
	}
	hd, err := old.Ledger.Hold(w.ctx, j.ID)
	w.must(err)
	if hd.State != "closing" {
		t.Fatalf("hold %s with the final settle's answer lost, want closing", hd.State)
	}

	w.clock.Advance(time.Minute)
	svc := w.restart()
	w.teardown(svc, j.ID)
	settles = w.console.CallsTo(consoletest.RouteSettle)
	if len(settles) != 2 || string(eventID(t, settles[0].Body)) != string(eventID(t, settles[1].Body)) {
		t.Fatalf("settles %d, want the same event re-sent once", len(settles))
	}
	w.assertBilledOnce(svc, j.ID, upAt, 250)
}

func field(t *testing.T, data []byte, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	v, _ := m[name].(string)
	return v
}

func eventID(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	return b.EventID
}

// A downtime the hold cannot cover and the console will not extend: the job
// is stopped as balance_exhausted, billed to where the hold ran out with H4's
// margin left, never past it, and the final settle fits inside the hold.
func TestRestartCapsADowntimeTheHoldCannotCover(t *testing.T) {
	w := newWorld(t)
	old := w.process(false)
	j := w.submit(old, 4*3600)
	upAt := w.up(old, j.ID)
	w.runFor(old, 10*time.Minute, 5*time.Minute)

	// 3 h down against a 2 h hold; every extend refused.
	w.clock.Advance(3 * time.Hour)
	w.console.InjectFault(consoletest.Fault{Route: consoletest.RouteExtend, Status: http.StatusPaymentRequired, Times: 100})
	svc := w.restart()
	w.step(svc)
	w.teardown(svc, j.ID)

	got := w.job(j.ID)
	if got.Status != store.StatusCancelled || got.Error == nil || got.Error.Code != string(ledger.EndBalanceExhausted) {
		t.Fatalf("job %s %+v, want cancelled/balance_exhausted", got.Status, got.Error)
	}
	// quota(s) <= hold − quota(600), largest such s.
	budget := quotaOf(7200) - quotaOf(600)
	var want int64
	for s := int64(0); quotaOf(s+1) <= budget; s++ {
		want = s + 1
	}
	w.assertBilledOnce(svc, j.ID, upAt, want)
	if want >= 7200-600+1 || want < 7200-600-1 {
		t.Fatalf("billed %d s, want about 6600", want)
	}
	types := w.eventTypes(j.ID)
	if types[len(types)-1] != "job.cancelled" {
		t.Fatalf("events %v", types)
	}
}

// Two processes on one database, as during a rolling restart: only the one
// holding the worker lock drives jobs. The other accepts submits and cancels
// and leaves them to the holder; when the holder goes away it takes over.
func TestTwoProcessesOnlyTheLockHolderDrives(t *testing.T) {
	w := newWorld(t)
	a, b := w.process(true), w.process(true)
	// A held lock pins a pooled connection, which closing the pool waits on.
	t.Cleanup(func() { a.Release(); b.Release() })
	if !a.Elect(w.ctx) {
		t.Fatal("first process did not get the worker lock")
	}
	if b.Elect(w.ctx) {
		t.Fatal("second process got the worker lock while the first holds it")
	}

	// A submit on b is persisted and held, but not launched by b.
	j := w.submit(b, 3600)
	b.Step(w.ctx)
	w.must(b.Reconcile(w.ctx))
	if j.Status != store.StatusQueued || len(w.sky.Launches()) != 0 || b.Backend.Tracked(j.ID) {
		t.Fatalf("job %s, %d launches, tracked by b %v", j.Status, len(w.sky.Launches()), b.Backend.Tracked(j.ID))
	}
	// a, driving, launches it on its next round.
	w.must(a.Reconcile(w.ctx))
	if w.launches(j.ID) != 1 || !a.Backend.Tracked(j.ID) {
		t.Fatalf("%d launches after the holder's round", w.launches(j.ID))
	}
	upAt := w.up(a, j.ID)
	w.runFor(a, 5*time.Minute, time.Minute)

	// b accepts a cancel; billing stops at b's acceptance, carried out by a.
	w.clock.Advance(20 * time.Second)
	accepted := w.clock.Now()
	cj, err := b.Cancel(w.ctx, owner, j.ID, accepted)
	w.must(err)
	if cj.Status != store.StatusRunning {
		t.Fatalf("cancel on the non-holder answered %s, want the job as it is", cj.Status)
	}
	b.Step(w.ctx)
	if got := w.job(j.ID).Status; got != store.StatusRunning {
		t.Fatalf("non-holder carried out the cancel: %s", got)
	}
	w.clock.Advance(40 * time.Second)
	w.must(a.Reconcile(w.ctx))
	w.teardown(a, j.ID)
	if got := w.job(j.ID).Status; got != store.StatusCancelled {
		t.Fatalf("job %s after the holder's round", got)
	}
	w.assertBilledOnce(a, j.ID, upAt, int64(accepted.Sub(upAt)/time.Second))

	// a goes away; b takes the lock and resumes a's running job.
	k := w.submit(a, 3600)
	upK := w.up(a, k.ID)
	w.runFor(a, 3*time.Minute, time.Minute)
	a.Release()
	if a.Reconcile(w.ctx); len(w.sky.Launches()) != 2 {
		t.Fatal("released process still launching")
	}
	if !b.Elect(w.ctx) {
		t.Fatal("second process did not get the lock once the first released it")
	}
	if !b.Backend.Tracked(k.ID) {
		t.Fatal("new holder did not resume the running job")
	}
	w.runFor(b, 2*time.Minute, time.Minute)
	end := upK.Add(5 * time.Minute)
	w.sky.SetJob(cluster(k.ID), "SUCCEEDED", nil, &end)
	w.step(b)
	w.teardown(b, k.ID)
	w.assertBilledOnce(b, k.ID, upK, 300)
	if n := w.launches(k.ID); n != 1 {
		t.Fatalf("%d launches of the handed-over job", n)
	}
}

// The lock itself: one holder per schema, released when its session ends
// even without an unlock, and a holder can tell that it lost it.
func TestWorkerLock(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	l1, ok, err := st.TryWorkerLock(ctx)
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if _, ok, err := st.TryWorkerLock(ctx); err != nil || ok {
		t.Fatalf("second lock while held: %v %v", ok, err)
	}
	if err := l1.Check(ctx); err != nil {
		t.Fatalf("holder's check: %v", err)
	}
	other := storetest.New(t)
	l3, ok, err := other.TryWorkerLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock in another schema: %v %v", ok, err)
	}
	l3.Release()

	// The holder's session dies, as when its process is killed.
	var pid int
	conn, err := st.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = conn.QueryRow(ctx, `SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 2 AND granted
		AND objid = hashtext(current_schema())::oid`).Scan(&pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	if err := l1.Check(ctx); err == nil {
		t.Fatal("holder's check passed after its session was terminated")
	}
	l1.Release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l2, ok, err := st.TryWorkerLock(ctx)
		if err == nil && ok {
			l2.Release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not free after the holder's session ended: %v %v", ok, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
