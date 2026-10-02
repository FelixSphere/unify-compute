package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// memStore enforces the same state diagram as the Postgres store.
type memStore struct {
	mu        sync.Mutex
	status    map[string]store.Status
	errs      map[string]*store.JobError
	placement map[string]store.Placement
	// order records "status:<s>" and "placement" writes as they happen.
	order []string
}

func newMemStore(ids ...string) *memStore {
	m := &memStore{status: map[string]store.Status{}, errs: map[string]*store.JobError{}, placement: map[string]store.Placement{}}
	for _, id := range ids {
		m.status[id] = store.StatusQueued
	}
	return m
}

func (m *memStore) UpdateStatus(_ context.Context, id string, to store.Status, jobErr *store.JobError) (store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	from, ok := m.status[id]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	if !store.CanTransition(from, to) {
		return store.Job{}, fmt.Errorf("%w: %s -> %s", store.ErrInvalidTransition, from, to)
	}
	m.status[id] = to
	if jobErr != nil {
		m.errs[id] = jobErr
	}
	m.order = append(m.order, "status:"+string(to))
	return store.Job{ID: id, Status: to}, nil
}

func (m *memStore) SetPlacement(_ context.Context, id string, p store.Placement) (store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status[id].Terminal() {
		return store.Job{}, store.ErrTerminal
	}
	m.placement[id] = p
	m.order = append(m.order, "placement")
	return store.Job{ID: id}, nil
}

func (m *memStore) get(id string) (store.Status, *store.JobError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status[id], m.errs[id]
}

type recSink struct {
	mu     sync.Mutex
	events []Event
	failOn EventKind
	fails  int
}

func (s *recSink) Emit(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Kind == s.failOn && s.fails > 0 {
		s.fails--
		return errors.New("ledger unavailable")
	}
	s.events = append(s.events, e)
	return nil
}

func (s *recSink) kinds() []EventKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []EventKind
	for _, e := range s.events {
		out = append(out, e.Kind)
	}
	return out
}

func (s *recSink) last(kind EventKind) (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].Kind == kind {
			return s.events[i], true
		}
	}
	return Event{}, false
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t     *testing.T
	fake  *skypilottest.Server
	store *memStore
	sink  *recSink
	clock *clock
	b     *Backend
	job   Job
	name  string
}

func newHarness(t *testing.T, mod ...func(*Job)) *harness {
	t.Helper()
	fake := skypilottest.New()
	fake.Token = "sky_test_token"
	t.Cleanup(fake.Close)
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	fake.SetClock(clk.now)
	j := Job{
		ID: "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", Attempt: 1,
		Spec:       Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: 7200},
		SKU:        sku.SKU{ID: "a10g-24gb-x1", Accelerator: "A10G", GPUCount: 1},
		Candidates: []sku.Provider{{Cloud: "aws", InstanceType: "g5.xlarge", Regions: []string{"us-east-1"}}},
	}
	for _, m := range mod {
		m(&j)
	}
	st, sink := newMemStore(j.ID), &recSink{}
	b := New(&Client{BaseURL: fake.URL, Token: "sky_test_token", UserHash: "unifycmp"}, st, sink, Config{Logf: t.Logf})
	b.now = clk.now
	return &harness{t: t, fake: fake, store: st, sink: sink, clock: clk, b: b, job: j, name: ClusterName(j.ID, 1)}
}

func (h *harness) launch() {
	h.t.Helper()
	if err := h.b.Launch(context.Background(), h.job); err != nil {
		h.t.Fatalf("launch: %v", err)
	}
}

func (h *harness) step() {
	h.t.Helper()
	if err := h.b.Step(context.Background(), h.job.ID); err != nil {
		h.t.Fatalf("step: %v", err)
	}
}

func (h *harness) wantStatus(want store.Status, code string) {
	h.t.Helper()
	got, jobErr := h.store.get(h.job.ID)
	if got != want {
		h.t.Fatalf("store status %s, want %s", got, want)
	}
	gotCode := ""
	if jobErr != nil {
		gotCode = jobErr.Code
	}
	if gotCode != code {
		h.t.Fatalf("error code %q, want %q", gotCode, code)
	}
}

func (h *harness) wantKinds(want ...EventKind) {
	h.t.Helper()
	if got := h.sink.kinds(); fmt.Sprint(got) != fmt.Sprint(want) {
		h.t.Fatalf("events %v, want %v", got, want)
	}
}

// up drives the attempt to cluster UP with the job submitted.
func (h *harness) up() time.Time {
	h.t.Helper()
	h.fake.Provision(h.name)
	h.step()
	h.fake.Up(h.name, "AWS", "us-east-1", "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)")
	h.clock.advance(90 * time.Second)
	upAt := h.clock.now()
	h.step()
	h.fake.SubmitJob(h.name)
	return upAt
}

// finishTeardown steps until the attempt is gone and the cluster is down.
func (h *harness) finishTeardown() {
	h.t.Helper()
	h.step()
	if _, err := h.b.Status(h.job.ID); !errors.Is(err, ErrUnknownJob) {
		h.t.Fatalf("attempt still tracked after teardown: %v", err)
	}
	if h.fake.Exists(h.name) {
		h.t.Fatal("cluster still exists after teardown")
	}
}

func TestHappyPathEmitsEveryEdgeAndRecordsPlacement(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.now()
	h.launch()
	h.wantStatus(store.StatusProvisioning, "")
	h.wantKinds(EventProvisioning)
	if ls := h.fake.Launches(); len(ls) != 1 || ls[0].Cluster != h.name {
		t.Fatalf("launches %+v", ls)
	} else if ls[0].Body["retry_until_up"] != false || ls[0].Task["run"] != "python run.py" {
		t.Fatalf("launch body %v", ls[0].Body)
	}

	h.fake.Provision(h.name)
	h.step() // INIT: still provisioning, nothing billed
	h.wantStatus(store.StatusProvisioning, "")
	h.wantKinds(EventProvisioning)

	h.fake.Up(h.name, "AWS", "us-east-1", "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)")
	h.clock.advance(90 * time.Second)
	upAt := h.clock.now()
	h.step()
	h.wantStatus(store.StatusRunning, "")
	h.wantKinds(EventProvisioning, EventClusterUp)
	up, _ := h.sink.last(EventClusterUp)
	want := Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"}
	if up.Placement == nil || *up.Placement != want || !up.At.Equal(upAt) {
		t.Fatalf("cluster_up %+v", up)
	}
	if h.store.placement[h.job.ID] != want {
		t.Fatalf("store placement %+v", h.store.placement[h.job.ID])
	}
	if fmt.Sprint(h.store.order) != "[status:provisioning placement status:running]" {
		t.Fatalf("placement must be written before running: %v", h.store.order)
	}

	h.fake.SubmitJob(h.name)
	started := upAt.Add(40 * time.Second)
	h.fake.SetJob(h.name, "RUNNING", &started, nil)
	h.clock.advance(time.Minute)
	h.step()
	h.wantKinds(EventProvisioning, EventClusterUp, EventJobStarted)
	if js, _ := h.sink.last(EventJobStarted); !js.At.Equal(started) {
		t.Fatalf("job_started at %v, want node time %v", js.At, started)
	}

	exited := upAt.Add(30 * time.Minute)
	h.fake.SetJob(h.name, "SUCCEEDED", nil, &exited)
	h.clock.advance(31 * time.Minute) // observed well after the exit
	h.step()
	h.wantStatus(store.StatusSucceeded, "")
	end, _ := h.sink.last(EventEnded)
	if end.Reason != EndExited || end.Status != store.StatusSucceeded || end.Failure != nil {
		t.Fatalf("ended %+v", end)
	}
	if !end.At.Equal(exited) || end.UpAt == nil || !end.UpAt.Equal(upAt) {
		t.Fatalf("ended must bill UP→process exit, not →observation: at=%v up=%v", end.At, end.UpAt)
	}
	if end.Placement == nil || *end.Placement != want {
		t.Fatalf("ended placement %+v", end.Placement)
	}

	h.finishTeardown()
	h.wantKinds(EventProvisioning, EventClusterUp, EventJobStarted, EventEnded, EventClusterDown)
	for _, e := range h.sink.events {
		if e.JobID != h.job.ID || e.Attempt != 1 || e.ID != eventID(h.job.ID, 1, e.Kind) {
			t.Fatalf("event identity %+v", e)
		}
		if e.At.Before(t0) {
			t.Fatalf("event %s before launch", e.Kind)
		}
	}
}

func TestCapacityExhaustedFailsFromProvisioningUnbilled(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.FailLaunch(h.name, "ResourcesUnavailableError",
		"Failed to provision all possible launchable resources. Relax the task's resource requirements: 1x AWS({'A10G': 1}, image_id=...)", false)
	h.step()
	h.wantStatus(store.StatusFailed, CodeCapacityExhausted)
	end, _ := h.sink.last(EventEnded)
	if end.Reason != EndProvisionFailed || end.UpAt != nil || !end.Failure.Retryable {
		t.Fatalf("B8: no UP, no GPU seconds, retryable: %+v", end)
	}
	h.finishTeardown() // /down answers ClusterDoesNotExist: already down
	h.wantKinds(EventProvisioning, EventEnded, EventClusterDown)
}

func TestImagePullFailureIsNotRetryableAndCleansUpTheInitCluster(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.Provision(h.name)
	h.step()
	h.fake.FailLaunch(h.name, "CommandError",
		"Command sudo docker pull ghcr.io/acme/embed:1.4 failed with return code 1.\nFailed to run docker setup: Error response from daemon: manifest unknown", true)
	h.step()
	h.wantStatus(store.StatusFailed, CodeImagePullFailed)
	if end, _ := h.sink.last(EventEnded); end.Failure.Retryable || end.UpAt != nil {
		t.Fatalf("%+v", end)
	}
	if !h.fake.Exists(h.name) {
		t.Fatal("precondition: SkyPilot leaves a failed cluster in INIT")
	}
	h.finishTeardown()
}

func TestSpotPreemptionEndsAtLastObservedUp(t *testing.T) {
	h := newHarness(t, func(j *Job) { j.SKU.Interruptible = true })
	h.launch()
	h.up()
	h.fake.SetJob(h.name, "RUNNING", nil, nil)
	h.clock.advance(5 * time.Minute)
	h.step()
	lastUp := h.clock.now()
	h.wantStatus(store.StatusRunning, "")

	h.fake.Vanish(h.name)
	h.clock.advance(5 * time.Minute)
	h.step()
	h.wantStatus(store.StatusInterrupted, CodePreempted)
	end, _ := h.sink.last(EventEnded)
	if end.Reason != EndPreempted || !end.At.Equal(lastUp) || !end.Failure.Retryable {
		t.Fatalf("I3: end is the last observed UP (%v), never detection: %+v", lastUp, end)
	}
	if !end.ObservedAt.After(end.At) {
		t.Fatalf("observed %v should be after %v", end.ObservedAt, end.At)
	}
	h.finishTeardown()
}

func TestOnDemandNodeLossIsProviderFault(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.up()
	h.fake.SetClusterStatus(h.name, "INIT") // abnormal-state refresh
	h.step()
	h.wantStatus(store.StatusInterrupted, CodeProviderFault)
	if end, _ := h.sink.last(EventEnded); end.Reason != EndProviderFault {
		t.Fatalf("%+v", end)
	}
	h.finishTeardown()
}

func TestMaxRuntimeBillsExactlyTheCap(t *testing.T) {
	h := newHarness(t, func(j *Job) { j.Spec.MaxRuntimeSeconds = 600 })
	h.launch()
	upAt := h.up()
	h.fake.SetJob(h.name, "RUNNING", nil, nil)
	h.clock.advance(10*time.Minute + 47*time.Second) // the poll lands late
	h.step()
	h.wantStatus(store.StatusFailed, CodeMaxRuntimeExceeded)
	end, _ := h.sink.last(EventEnded)
	if end.Reason != EndMaxRuntime || !end.At.Equal(upAt.Add(600*time.Second)) {
		t.Fatalf("B16: billed to exactly max_runtime: %+v (up %v)", end, upAt)
	}
	h.finishTeardown()
}

func TestMaxRuntimeDuringSetupCancelsTheLaunch(t *testing.T) {
	h := newHarness(t, func(j *Job) { j.Spec.MaxRuntimeSeconds = 60 })
	h.launch()
	h.fake.Up(h.name, "AWS", "us-east-1", "")
	h.step()
	h.clock.advance(2 * time.Minute) // setup still running, launch not returned
	h.step()
	if got := h.fake.LaunchStatus(h.name); got != "CANCELLED" {
		t.Fatalf("launch request %s; a launch left running could start the job after our down", got)
	}
	h.finishTeardown()
}

func TestNonZeroExitFailsAndBillsToExit(t *testing.T) {
	h := newHarness(t)
	h.launch()
	upAt := h.up()
	exit := upAt.Add(3 * time.Minute)
	h.fake.SetJob(h.name, "FAILED", &upAt, &exit)
	h.step()
	h.wantStatus(store.StatusFailed, CodeNonZeroExit)
	if end, _ := h.sink.last(EventEnded); !end.At.Equal(exit) || end.Failure.Retryable {
		t.Fatalf("B7: %+v", end)
	}
	h.finishTeardown()
}

func TestDriverLossIsInterrupted(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.up()
	h.fake.SetJob(h.name, "FAILED_DRIVER", nil, nil)
	h.step()
	h.wantStatus(store.StatusInterrupted, CodeProviderFault)
}

func TestLaunchFailureAfterUpIsBilledFromUp(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.Up(h.name, "AWS", "us-east-1", "")
	h.step()
	h.fake.FailLaunch(h.name, "CommandError", "Failed to sync file mounts: s3://acme-data/docs: AccessDenied", true)
	h.step()
	h.wantStatus(store.StatusFailed, CodeProvisionFailed)
	if end, _ := h.sink.last(EventEnded); end.UpAt == nil {
		t.Fatal("the GPU was the customer's from UP; the attempt must carry UpAt")
	}
}

func TestCancelWhileProvisioning(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.Provision(h.name)
	h.step()
	accepted := h.clock.now()
	if err := h.b.Cancel(context.Background(), h.job.ID, accepted); err != nil {
		t.Fatal(err)
	}
	h.wantStatus(store.StatusCancelled, "")
	end, _ := h.sink.last(EventEnded)
	if end.Reason != EndCancelled || end.UpAt != nil {
		t.Fatalf("B10: %+v", end)
	}
	if h.fake.LaunchStatus(h.name) != "CANCELLED" {
		t.Fatal("launch request still running")
	}
	h.finishTeardown()
	if err := h.b.Cancel(context.Background(), h.job.ID, accepted); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("cancel after teardown: %v", err)
	}
}

func TestCancelWhileRunningStopsAtAcceptance(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.up()
	h.fake.SetJob(h.name, "RUNNING", nil, nil)
	h.clock.advance(20 * time.Minute)
	accepted := h.clock.now()
	h.clock.advance(3 * time.Second) // cancel latency is ours
	if err := h.b.Cancel(context.Background(), h.job.ID, accepted); err != nil {
		t.Fatal(err)
	}
	h.wantStatus(store.StatusCancelled, "")
	if end, _ := h.sink.last(EventEnded); !end.At.Equal(accepted) || end.UpAt == nil {
		t.Fatalf("B11: %+v", end)
	}
	if err := h.b.Cancel(context.Background(), h.job.ID, accepted); !errors.Is(err, ErrAlreadyEnded) {
		t.Fatalf("second cancel: %v", err)
	}
	h.finishTeardown()
}

func TestSinkOutageDelaysButNeverDropsOrReordersAnEdge(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.Up(h.name, "AWS", "us-east-1", "")
	upAt := h.clock.now()
	h.sink.failOn, h.sink.fails = EventClusterUp, 2
	if err := h.b.Step(context.Background(), h.job.ID); err == nil {
		t.Fatal("want the emit error surfaced")
	}
	h.wantStatus(store.StatusProvisioning, "") // ledger first: no running without the event
	h.clock.advance(time.Minute)
	if err := h.b.Step(context.Background(), h.job.ID); err == nil {
		t.Fatal("still failing")
	}
	h.step()
	h.wantStatus(store.StatusRunning, "")
	up, _ := h.sink.last(EventClusterUp)
	if !up.At.Equal(upAt) {
		t.Fatalf("a retried edge keeps its original time: %v, want %v", up.At, upAt)
	}
	h.wantKinds(EventProvisioning, EventClusterUp)
}

func TestFailedDownIsRetriedAndClusterDownWaitsForIt(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.up()
	exit := h.clock.now()
	h.fake.SetJob(h.name, "SUCCEEDED", nil, &exit)
	h.step()
	h.fake.FailDowns(1)
	if err := h.b.Step(context.Background(), h.job.ID); err == nil {
		t.Fatal("want down failure surfaced")
	}
	if _, ok := h.sink.last(EventClusterDown); ok {
		t.Fatal("cluster_down before SkyPilot confirmed it")
	}
	h.finishTeardown()
	h.wantKinds(EventProvisioning, EventClusterUp, EventEnded, EventClusterDown)
}

func TestLaunchPostFailureIsRetriedOnStep(t *testing.T) {
	h := newHarness(t)
	h.fake.FailNext("/launch", http.StatusBadGateway)
	h.launch()
	h.wantStatus(store.StatusProvisioning, "")
	if n := len(h.fake.Launches()); n != 0 {
		t.Fatalf("%d launches", n)
	}
	h.step()
	if n := len(h.fake.Launches()); n != 1 {
		t.Fatalf("%d launches after retry", n)
	}
}

// A /launch whose answer was lost must be adopted, not sent again: a second
// launch on the same cluster name would run the task twice on one cluster.
func TestLaunchAdoptsAnExistingLaunchRequest(t *testing.T) {
	h := newHarness(t)
	task, _ := TaskFor(h.job, TaskOptions{})
	c := &Client{BaseURL: h.fake.URL, Token: "sky_test_token", UserHash: "unifycmp"}
	if _, err := c.Launch(context.Background(), h.name, task); err != nil {
		t.Fatal(err)
	}
	h.launch()
	if n := len(h.fake.Launches()); n != 1 {
		t.Fatalf("%d launches, want the existing one adopted", n)
	}
	h.fake.Up(h.name, "AWS", "us-east-1", "")
	h.step()
	h.wantStatus(store.StatusRunning, "")
}

func TestLaunchRefusedWhenJobNoLongerQueued(t *testing.T) {
	h := newHarness(t)
	if _, err := h.store.UpdateStatus(context.Background(), h.job.ID, store.StatusCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.b.Launch(context.Background(), h.job); !errors.Is(err, store.ErrInvalidTransition) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.fake.Launches()); n != 0 {
		t.Fatalf("a cancelled job was launched")
	}
	if err := h.b.Launch(context.Background(), h.job); errors.Is(err, ErrDuplicate) {
		t.Fatal("a refused launch must not stay tracked")
	}
}

func TestLaunchRefusesUntranslatableSpecWithoutWriting(t *testing.T) {
	h := newHarness(t, func(j *Job) { j.Spec.Outputs = []Output{{Mount: "/mnt/out"}} })
	if err := h.b.Launch(context.Background(), h.job); !errors.Is(err, ErrOutputsUnsupported) {
		t.Fatalf("err = %v", err)
	}
	h.wantStatus(store.StatusQueued, "")
	h.wantKinds()
}

func TestLogsDropControlLinesAndReportMissingCluster(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.up()
	h.fake.SetLogs(h.name, "epoch 1 loss 0.42\n<rich_update>Waiting</rich_update>\nepoch 2 loss 0.31\n")
	var buf bytes.Buffer
	if err := h.b.Logs(context.Background(), h.job.ID, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "epoch 1 loss 0.42\nepoch 2 loss 0.31\n" {
		t.Fatalf("logs %q", buf.String())
	}
	h.fake.Vanish(h.name)
	err := h.b.Logs(context.Background(), h.job.ID, &buf)
	var re *RequestError
	if !errors.As(err, &re) || re.Type != "ClusterDoesNotExist" {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryCallCarriesAuthAndVersionHeaders(t *testing.T) {
	h := newHarness(t)
	h.fake.Token = "other"
	if err := h.b.Launch(context.Background(), h.job); err != nil {
		t.Fatal(err)
	}
	if err := h.b.Step(context.Background(), h.job.ID); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 surfaced, got %v", err)
	}
}

func TestRunStepsUntilCancelled(t *testing.T) {
	h := newHarness(t)
	h.launch()
	h.fake.FailLaunch(h.name, "ResourcesUnavailableError", "no capacity", false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.b.Run(ctx, time.Millisecond); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := h.b.Status(h.job.ID); errors.Is(err, ErrUnknownJob) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt never finished under Run")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	h.wantStatus(store.StatusFailed, CodeCapacityExhausted)
}
