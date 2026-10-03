// Package backend runs compute jobs on SkyPilot (ADR-0003) through its API
// server's HTTP API: launch, observe, collect logs, cancel and tear down, one
// cluster per job attempt. It maps SkyPilot's cluster and job states onto the
// job state diagram in docs/spec/job-api.md and emits the lifecycle events
// metering keys off. The design and the wire contract are
// docs/spec/backend.md.
//
// compute-api's jobs package wires it to the submit handler, the ledger and
// the event log.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Store is the subset of *store.Store the backend writes.
type Store interface {
	UpdateStatus(ctx context.Context, id string, to store.Status, jobErr *store.JobError) (store.Job, error)
	SetPlacement(ctx context.Context, id string, p store.Placement) (store.Job, error)
}

type Config struct {
	Task TaskOptions
	// Logf receives errors the poll loop recovers from. Nil discards them.
	Logf func(format string, args ...any)
	// Now is the clock edges are timed by; nil is time.Now.
	Now func() time.Time
}

var (
	ErrUnknownJob   = errors.New("backend: job is not tracked by this backend")
	ErrAlreadyEnded = errors.New("backend: attempt already ended")
	ErrDuplicate    = errors.New("backend: attempt already launched")
)

type phase int

const (
	phaseSubmit    phase = iota // provisioning recorded; /launch not yet accepted
	phaseLaunching              // launch request in flight, cluster not yet seen UP
	phaseUp                     // cluster seen UP: the GPU meter is running
	phaseTeardown               // ended; waiting for SkyPilot to confirm down
	phaseDone
)

var phaseNames = map[phase]string{
	phaseSubmit: "submit", phaseLaunching: "launching", phaseUp: "up", phaseTeardown: "teardown", phaseDone: "done",
}

// outboxItem is one lifecycle edge still to be made durable: the event to the
// sink first (metering is the source of truth), then the store write. Items
// are retried in order until both have succeeded, so a sink or database
// outage delays an edge but never loses or reorders one.
type outboxItem struct {
	event   Event
	emitted bool
	write   func(ctx context.Context) error
}

type attempt struct {
	mu      sync.Mutex
	job     Job
	cluster string
	task    Task
	phase   phase

	launchReq      string
	launchFinished bool // the launch request reached a final state
	launchDone     bool // ... and succeeded, so skyJobID is known
	skyJobID       *int

	upAt, lastUpAt time.Time
	placement      *Placement
	started        bool
	downReq        string
	ended          *Event

	// resumed: tracked from durable state by Resume, not launched by this
	// process. The first observation checks the cluster is still the one
	// that was seen UP.
	resumed bool
	// pendingRunning: cluster_up reached the ledger but the store never
	// moved to running; the edge's store write is redone on the next UP.
	pendingRunning bool

	outbox []*outboxItem
}

type Backend struct {
	api   *Client
	store Store
	sink  Sink
	cfg   Config
	now   func() time.Time

	mu       sync.Mutex
	attempts map[string]*attempt
}

func New(api *Client, st Store, sink Sink, cfg Config) *Backend {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Backend{api: api, store: st, sink: sink, cfg: cfg, now: now, attempts: map[string]*attempt{}}
}

func (b *Backend) logf(format string, args ...any) {
	if b.cfg.Logf != nil {
		b.cfg.Logf(format, args...)
	}
}

// Launch starts an attempt. The job moves queued → provisioning in the store
// before anything reaches SkyPilot, so a job cancelled in the meantime is
// refused here rather than launched. A translation error is returned with
// nothing written: the spec the handler accepted cannot be run, which is a
// validation gap to fix, not a job state.
//
// The /launch POST itself may fail (API server unreachable); the attempt is
// still tracked and the next Step retries it, adopting a launch whose answer
// was lost instead of launching twice.
func (b *Backend) Launch(ctx context.Context, j Job) error {
	if j.Attempt < 1 {
		j.Attempt = 1
	}
	task, err := TaskFor(j, b.cfg.Task)
	if err != nil {
		return err
	}
	b.mu.Lock()
	if _, ok := b.attempts[j.ID]; ok {
		b.mu.Unlock()
		return ErrDuplicate
	}
	a := &attempt{job: j, cluster: ClusterName(j.ID, j.Attempt), task: task}
	a.mu.Lock()
	defer a.mu.Unlock()
	b.attempts[j.ID] = a
	b.mu.Unlock()

	// A retry attempt enters from interrupted, which the state diagram does
	// not allow back to provisioning; retry is not built, so only the first
	// attempt moves the job here.
	if j.Attempt == 1 {
		if _, err := b.store.UpdateStatus(ctx, j.ID, store.StatusProvisioning, nil); err != nil {
			b.mu.Lock()
			delete(b.attempts, j.ID)
			b.mu.Unlock()
			return err
		}
	}
	now := b.now()
	a.push(Event{Kind: EventProvisioning, At: now, ObservedAt: now}, nil)
	if err := b.flush(ctx, a); err != nil {
		b.logf("backend: %s: %v", j.ID, err)
	}
	if err := b.submit(ctx, a); err != nil {
		b.logf("backend: %s: launch: %v (retried on next step)", j.ID, err)
	}
	return nil
}

func (a *attempt) push(e Event, write func(context.Context) error) {
	e.JobID, e.Attempt = a.job.ID, a.job.Attempt
	e.ID = eventID(e.JobID, e.Attempt, e.Kind)
	if e.Placement == nil && a.placement != nil {
		p := *a.placement
		e.Placement = &p
	}
	a.outbox = append(a.outbox, &outboxItem{event: e, write: write})
}

func (b *Backend) flush(ctx context.Context, a *attempt) error {
	for len(a.outbox) > 0 {
		it := a.outbox[0]
		if !it.emitted {
			if err := b.sink.Emit(ctx, it.event); err != nil {
				return fmt.Errorf("emit %s: %w", it.event.ID, err)
			}
			it.emitted = true
		}
		if it.write != nil {
			if err := it.write(ctx); err != nil {
				return fmt.Errorf("store after %s: %w", it.event.ID, err)
			}
		}
		a.outbox = a.outbox[1:]
	}
	return nil
}

func (b *Backend) submit(ctx context.Context, a *attempt) error {
	info, found, err := b.api.LaunchRequestFor(ctx, a.cluster)
	if err != nil {
		return err
	}
	if found {
		a.launchReq = info.ID
	} else {
		id, err := b.api.Launch(ctx, a.cluster, a.task)
		if err != nil {
			return err
		}
		a.launchReq = id
	}
	a.phase = phaseLaunching
	return nil
}

// Step observes one attempt once and advances it. The poll loop calls it;
// tests call it directly to drive the state machine one observation at a
// time.
func (b *Backend) Step(ctx context.Context, jobID string) error {
	a, ok := b.get(jobID)
	if !ok {
		return ErrUnknownJob
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := b.flush(ctx, a); err != nil {
		return err
	}
	var err error
	switch a.phase {
	case phaseSubmit:
		err = b.submit(ctx, a)
	case phaseLaunching:
		err = b.stepLaunching(ctx, a)
	case phaseUp:
		err = b.stepUp(ctx, a)
	case phaseTeardown:
		err = b.stepTeardown(ctx, a)
	}
	if ferr := b.flush(ctx, a); err == nil {
		err = ferr
	}
	if a.phase == phaseDone && len(a.outbox) == 0 {
		b.mu.Lock()
		delete(b.attempts, jobID)
		b.mu.Unlock()
	}
	return err
}

func (b *Backend) get(jobID string) (*attempt, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.attempts[jobID]
	return a, ok
}

func (b *Backend) cluster(ctx context.Context, a *attempt) (ClusterRecord, bool, error) {
	recs, err := b.api.Status(ctx, a.cluster)
	if err != nil {
		return ClusterRecord{}, false, err
	}
	for _, r := range recs {
		if r.Name == a.cluster {
			return r, true, nil
		}
	}
	return ClusterRecord{}, false, nil
}

func (b *Backend) stepLaunching(ctx context.Context, a *attempt) error {
	rec, present, err := b.cluster(ctx, a)
	if err != nil {
		return err
	}
	if present && rec.Status == ClusterUp {
		b.observeUp(a, rec)
		return nil
	}
	ri, err := b.api.RequestState(ctx, a.launchReq)
	if errors.Is(err, ErrRequestNotFound) {
		// The server lost the request (its request table is not durable
		// across a redeploy). Launch again; the cluster name is the same.
		a.phase = phaseSubmit
		return b.submit(ctx, a)
	}
	if err != nil {
		return err
	}
	a.launchFinished = ri.Status.Finished()
	switch ri.Status {
	case RequestFailed:
		f := classifyLaunchError(b.api.Get(ctx, a.launchReq, nil), a.job.SKU.Interruptible)
		status := store.StatusFailed
		if f.Code == CodePreempted {
			status = store.StatusInterrupted
		}
		b.end(ctx, a, EndProvisionFailed, b.now(), status, f)
	case RequestCancelled:
		b.end(ctx, a, EndProvisionFailed, b.now(), store.StatusFailed,
			&Failure{Code: CodeProvisionFailed, Message: "the launch was cancelled on the SkyPilot API server", Retryable: true})
	case RequestSucceeded:
		// Launch returns once the job is submitted, after UP. Succeeded
		// with the cluster absent means it was torn down underneath us
		// between our two reads.
		if !present {
			b.end(ctx, a, EndProviderFault, b.now(), store.StatusInterrupted,
				&Failure{Code: CodeProviderFault, Message: "the cluster disappeared before it was observed up", Retryable: true})
		}
	}
	return nil
}

func (b *Backend) observeUp(a *attempt, rec ClusterRecord) {
	now := b.now()
	p := placementFrom(rec, a.job.Candidates)
	a.placement = &p
	a.upAt, a.lastUpAt = now, now
	a.phase = phaseUp
	id := a.job.ID
	// metering.md "Start": the time compute-api observes UP, not SkyPilot's
	// status_updated_at. Placement is written before running so a reader
	// never sees a running job without one (job-api.md).
	a.push(Event{Kind: EventClusterUp, At: now, ObservedAt: now, UpAt: &now}, func(ctx context.Context) error {
		if _, err := b.store.SetPlacement(ctx, id, p); err != nil && !errors.Is(err, store.ErrTerminal) {
			return err
		}
		_, err := b.store.UpdateStatus(ctx, id, store.StatusRunning, nil)
		if errors.Is(err, store.ErrInvalidTransition) {
			b.logf("backend: %s: running: %v", id, err)
			return nil
		}
		return err
	})
}

// redoClusterUp repeats a resumed attempt's cluster_up edge: the same event
// (same ID and At, which the sink has already recorded) and the store write
// that did not happen, placement and then running.
func (b *Backend) redoClusterUp(a *attempt, rec ClusterRecord) {
	p := placementFrom(rec, a.job.Candidates)
	a.placement = &p
	up, id := a.upAt, a.job.ID
	a.push(Event{Kind: EventClusterUp, At: up, ObservedAt: up, UpAt: &up}, func(ctx context.Context) error {
		if _, err := b.store.SetPlacement(ctx, id, p); err != nil && !errors.Is(err, store.ErrTerminal) {
			return err
		}
		_, err := b.store.UpdateStatus(ctx, id, store.StatusRunning, nil)
		if errors.Is(err, store.ErrInvalidTransition) {
			b.logf("backend: %s: running: %v", id, err)
			return nil
		}
		return err
	})
}

type launchResult struct {
	JobID *int `json:"job_id"`
}

func (b *Backend) stepUp(ctx context.Context, a *attempt) error {
	now := b.now()
	limit := a.job.runtimeLimit()
	deadline := a.upAt.Add(time.Duration(limit) * time.Second)
	// finish ends the attempt at `at`, unless the cap came first: then it is
	// max_runtime at exactly the cap (B16), however late this observation.
	// The cap is checked against when things happened, not when we looked,
	// so a job that exited before its cap but was observed after it still
	// succeeds.
	finish := func(reason EndReason, at time.Time, status store.Status, f *Failure) {
		if at.After(deadline) {
			reason, at, status = EndMaxRuntime, deadline, store.StatusFailed
			f = &Failure{Code: CodeMaxRuntimeExceeded, Message: fmt.Sprintf("the job ran for its max_runtime_seconds (%d)", a.job.Spec.MaxRuntimeSeconds)}
			if limit != a.job.Spec.MaxRuntimeSeconds {
				f.Message = fmt.Sprintf("the job ran for the %d s its hold still covered when it was launched late after a compute-api restart (max_runtime_seconds %d)", limit, a.job.Spec.MaxRuntimeSeconds)
			}
		}
		b.end(ctx, a, reason, at, status, f)
	}
	pastCap := func(err error) error {
		if !now.Before(deadline) {
			finish(EndMaxRuntime, now, store.StatusFailed, nil)
			return nil
		}
		return err
	}

	rec, present, err := b.cluster(ctx, a)
	if err != nil {
		return pastCap(err)
	}
	if present && a.resumed && rec.LaunchedAt > a.upAt.Unix() {
		// Launched after we saw this attempt UP: a different cluster under
		// the same name, so ours did not stay UP while nobody watched.
		b.logf("backend: %s: cluster %s relaunched at %d, after its UP at %s", a.job.ID, a.cluster, rec.LaunchedAt, a.upAt.Format(time.RFC3339))
		present = false
	}
	if !present || rec.Status != ClusterUp {
		// metering.md end rule 4 / I3: the end is the last observation of
		// UP, never when we noticed it gone. SkyPilot does not report a
		// preemption time on the cluster record.
		reason, code, msg := EndProviderFault, CodeProviderFault, "the instance stopped responding"
		if a.job.SKU.Interruptible {
			reason, code, msg = EndPreempted, CodePreempted, "the spot instance was preempted"
		}
		finish(reason, a.lastUpAt, store.StatusInterrupted, &Failure{Code: code, Message: msg, Retryable: true})
		return nil
	}
	a.lastUpAt = now
	a.resumed = false
	if a.pendingRunning {
		a.pendingRunning = false
		b.redoClusterUp(a, rec)
	}

	if !a.launchDone {
		ri, err := b.api.RequestState(ctx, a.launchReq)
		if err != nil {
			return pastCap(err)
		}
		a.launchFinished = ri.Status.Finished()
		switch ri.Status {
		case RequestSucceeded:
			var lr launchResult
			if err := b.api.Get(ctx, a.launchReq, &lr); err != nil {
				return pastCap(err)
			}
			a.skyJobID, a.launchDone = lr.JobID, true
		case RequestFailed, RequestCancelled:
			// Failed after UP and before the job was queued: file mounts
			// (a customer bucket we cannot read) or runtime setup. The
			// GPU was the customer's from UP, so this is billed (B7).
			f := classifyLaunchError(b.api.Get(ctx, a.launchReq, nil), a.job.SKU.Interruptible)
			finish(EndExited, now, store.StatusFailed, f)
			return nil
		}
	}

	jobs, err := b.api.Queue(ctx, a.cluster)
	if err != nil {
		return pastCap(err)
	}
	if j, ok := pickJob(jobs, a.skyJobID); ok {
		if !a.started && j.StartAt != nil {
			a.started = true
			a.push(Event{Kind: EventJobStarted, At: unixFloat(*j.StartAt), ObservedAt: now}, nil)
		}
		if j.Status.Terminal() {
			reason, status, f := jobOutcome(j.Status)
			at := now
			if j.EndAt != nil {
				at = unixFloat(*j.EndAt)
			}
			if at.Before(a.upAt) {
				at = a.upAt
			}
			finish(reason, at, status, f)
			return nil
		}
	}
	if !now.Before(deadline) {
		finish(EndMaxRuntime, now, store.StatusFailed, nil)
		return nil
	}
	// Only now is the attempt known to be still the customer's at `now`: the
	// cluster is UP and the job has not ended. Reporting UP earlier in the
	// step could let metering run past an exit this same step discovers.
	if o, ok := b.sink.(UpObserver); ok {
		if err := o.ObservedUp(ctx, a.job.ID, a.job.Attempt, now); err != nil {
			b.logf("backend: %s: report up: %v", a.job.ID, err)
		}
	}
	return nil
}

// pickJob finds the attempt's job in the cluster queue: the id the launch
// returned, or before that is known, the newest (the cluster runs only ours).
func pickJob(jobs []ClusterJob, id *int) (ClusterJob, bool) {
	if len(jobs) == 0 {
		return ClusterJob{}, false
	}
	if id != nil {
		for _, j := range jobs {
			if j.JobID == *id {
				return j, true
			}
		}
		return ClusterJob{}, false
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].JobID > jobs[k].JobID })
	return jobs[0], true
}

// end decides the attempt's outcome exactly once and moves it to teardown.
// The cluster is always torn down explicitly: SkyPilot's autodown does not
// fire after a failed provision or setup ("the cluster will not be torn down
// for debugging purposes", execution.launch), and a cluster left in INIT is
// still billed to us.
func (b *Backend) end(ctx context.Context, a *attempt, reason EndReason, at time.Time, status store.Status, f *Failure) {
	if a.ended != nil {
		return
	}
	if a.launchReq == "" {
		// The /launch answer was never seen. If the POST did reach the
		// server, that launch is running unowned; find it so it is stopped.
		if info, found, err := b.api.LaunchRequestFor(ctx, a.cluster); err == nil && found && !info.Status.Finished() {
			a.launchReq = info.ID
		}
	}
	if a.launchReq != "" && !a.launchFinished {
		// Still provisioning or setting up: stop it before tearing down,
		// or the launch could finish and start the job after our down.
		if err := b.api.CancelRequest(ctx, a.launchReq); err != nil {
			b.logf("backend: %s: cancel launch request: %v", a.job.ID, err)
		}
	}
	e := Event{Kind: EventEnded, At: at, ObservedAt: b.now(), Reason: reason, Status: status, Failure: f}
	if a.phase == phaseUp {
		up := a.upAt
		e.UpAt = &up
	}
	a.ended = &e
	a.phase = phaseTeardown
	id, jobErr := a.job.ID, f.jobError()
	a.push(e, func(ctx context.Context) error {
		_, err := b.store.UpdateStatus(ctx, id, status, jobErr)
		if errors.Is(err, store.ErrInvalidTransition) {
			b.logf("backend: %s: %s: %v", id, status, err)
			return nil
		}
		return err
	})
}

func (b *Backend) stepTeardown(ctx context.Context, a *attempt) error {
	if a.downReq == "" {
		id, err := b.api.Down(ctx, a.cluster)
		if err != nil {
			return err
		}
		a.downReq = id
	}
	ri, err := b.api.RequestState(ctx, a.downReq)
	if err != nil {
		if errors.Is(err, ErrRequestNotFound) {
			a.downReq = ""
		}
		return err
	}
	switch ri.Status {
	case RequestSucceeded:
		b.confirmDown(a, ri)
	case RequestFailed:
		err := b.api.Get(ctx, a.downReq, nil)
		var re *RequestError
		if errors.As(err, &re) && re.Type == "ClusterDoesNotExist" {
			b.confirmDown(a, ri)
			return nil
		}
		a.downReq = ""
		return fmt.Errorf("down %s: %w", a.cluster, err)
	case RequestCancelled:
		a.downReq = ""
	}
	return nil
}

func (b *Backend) confirmDown(a *attempt, ri RequestInfo) {
	now := b.now()
	at := now
	if ri.FinishedAt != nil {
		at = *ri.FinishedAt
	}
	a.push(Event{Kind: EventClusterDown, At: at, ObservedAt: now}, nil)
	a.phase = phaseDone
}

// Cancel ends the attempt as cancelled at acceptedAt — the moment the API
// accepted the customer's cancel, which is where GPU billing stops (B11), not
// when the instance is confirmed down — and starts teardown.
func (b *Backend) Cancel(ctx context.Context, jobID string, acceptedAt time.Time) error {
	return b.stop(ctx, jobID, acceptedAt, nil)
}

// Stop is Cancel on compute-api's own initiative (metering.md B12, B13: the
// hold can no longer cover the job): the job ends cancelled at `at` with f as
// its error, so the customer sees why.
func (b *Backend) Stop(ctx context.Context, jobID string, at time.Time, f *Failure) error {
	return b.stop(ctx, jobID, at, f)
}

func (b *Backend) stop(ctx context.Context, jobID string, acceptedAt time.Time, f *Failure) error {
	a, ok := b.get(jobID)
	if !ok {
		return ErrUnknownJob
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ended != nil {
		return ErrAlreadyEnded
	}
	if a.phase == phaseUp && acceptedAt.Before(a.upAt) {
		acceptedAt = a.upAt
	}
	b.end(ctx, a, EndCancelled, acceptedAt, store.StatusCancelled, f)
	err := b.flush(ctx, a)
	if terr := b.stepTeardown(ctx, a); err == nil {
		err = terr
	}
	if ferr := b.flush(ctx, a); err == nil {
		err = ferr
	}
	return err
}

// Logs writes the attempt's stdout/stderr so far. It works while the cluster
// exists; logs after teardown need the copy-before-down step that is an open
// question in backend.md.
func (b *Backend) Logs(ctx context.Context, jobID string, w io.Writer) error {
	a, ok := b.get(jobID)
	if !ok {
		return ErrUnknownJob
	}
	a.mu.Lock()
	cluster, id := a.cluster, a.skyJobID
	a.mu.Unlock()
	return b.api.Logs(ctx, cluster, id, w)
}

// Snapshot is the backend's view of an attempt.
type Snapshot struct {
	JobID     string
	Attempt   int
	Cluster   string
	Phase     string
	Placement *Placement
	UpAt      *time.Time
	Ended     *Event
	// Pending is how many lifecycle edges are decided but not yet durable.
	Pending int
}

func (b *Backend) Status(jobID string) (Snapshot, error) {
	a, ok := b.get(jobID)
	if !ok {
		return Snapshot{}, ErrUnknownJob
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Snapshot{JobID: a.job.ID, Attempt: a.job.Attempt, Cluster: a.cluster, Phase: phaseNames[a.phase], Pending: len(a.outbox)}
	if a.placement != nil {
		p := *a.placement
		s.Placement = &p
	}
	if a.ended != nil {
		e := *a.ended
		s.Ended = &e
	}
	if !a.upAt.IsZero() {
		up := a.upAt
		s.UpAt = &up
	}
	return s, nil
}

// Run steps every tracked attempt each interval until ctx ends. Errors are
// logged and the attempt is retried on the next tick.
func (b *Backend) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b.StepAll(ctx)
	}
}

// StepAll steps every tracked attempt once, logging what fails; the attempt
// is retried on the next call.
func (b *Backend) StepAll(ctx context.Context) {
	b.mu.Lock()
	ids := make([]string, 0, len(b.attempts))
	for id := range b.attempts {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := b.Step(ctx, id); err != nil && !errors.Is(err, ErrUnknownJob) {
			b.logf("backend: %s: %v", id, err)
		}
	}
}
