package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// The numbers resuming a job keys off (backend.md, "Restart").
const (
	// SettlementReserve is the part of a hold's H6 grace (48 h) kept for
	// settling after the job ends. The other day is what a job may wait to
	// start: one launched later gets only the runtime its hold still covers.
	SettlementReserve = 24 * time.Hour
	// MinLaunchRuntime is the least runtime worth provisioning a GPU for
	// when a late launch would be cut short; below it the job is not
	// started. A job asking for less than this is held to its own ask.
	MinLaunchRuntime = 300
	// LockRetry is how often a process without the worker lock tries for it,
	// and LockCheck how often the holder confirms it still has it.
	LockRetry = 5 * time.Second
	LockCheck = 5 * time.Second
	// TeardownLookback is how far back a new holder looks for ended jobs
	// whose cluster may still be up. Far beyond SkyPilot's 5-minute idle
	// autodown, which covers a job that exited; not a cancelled one.
	TeardownLookback = 24 * time.Hour
)

// CodeQueueExpired is the error.code of a job cancelled because it could not
// be started within its hold (events.md: job.cancelled reason).
const CodeQueueExpired = "queue_expired"

const queueExpiredMessage = "Not started: compute-api was unavailable for longer than the job's hold allows a start after submit. Nothing was charged; submit it again."

// Reconcile takes up every live job this process is not driving, from what
// is durable: the store's status, the ledger's attempts (UP time and last
// observed UP), and SkyPilot's own view of the cluster, which each resumed
// attempt is stepped against at once. It then carries out cancels accepted
// by any process. Only the worker-lock holder calls it; at boot it runs
// before the workers start.
//
// A job that cannot be taken up now (SkyPilot unreachable, say) is left as
// it is and tried again on the next call.
func (s *Service) Reconcile(ctx context.Context) error {
	if !s.driving() {
		return nil
	}
	live, err := s.Store.ActiveJobs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	active := make(map[string]store.Job, len(live))
	for _, j := range live {
		active[j.ID] = j
		if s.Backend == nil || s.Backend.Tracked(j.ID) {
			continue
		}
		if err := s.resume(ctx, j); err != nil {
			errs = append(errs, fmt.Errorf("resume %s: %w", j.ID, err))
		}
	}
	errs = append(errs, s.carryOutCancels(ctx, active))
	if s.Backend != nil && !s.swept {
		err := s.tearDownLeftovers(ctx)
		s.swept = err == nil
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// tearDownLeftovers runs once per term as the lock holder: an earlier
// process may have ended a job and died before SkyPilot confirmed its
// cluster down.
func (s *Service) tearDownLeftovers(ctx context.Context) error {
	fin, err := s.Store.RecentlyFinished(ctx, TeardownLookback)
	if err != nil {
		return err
	}
	var js []backend.Job
	for _, j := range fin {
		attempt := 1
		if atts, err := s.Ledger.Attempts(ctx, j.ID); err == nil && len(atts) > 0 {
			attempt = atts[len(atts)-1].Attempt
		}
		js = append(js, backend.Job{ID: j.ID, Attempt: attempt})
	}
	n, err := s.Backend.TearDownLeftovers(ctx, js)
	if n > 0 {
		s.log.Warn("tearing down clusters of jobs that ended under an earlier process", "clusters", n)
	}
	return err
}

func (s *Service) resume(ctx context.Context, j store.Job) error {
	var spec backend.Spec
	if err := json.Unmarshal(j.Spec, &spec); err != nil {
		return fmt.Errorf("spec: %w", err)
	}
	item, err := s.catalogue.Get(j.SKU)
	if err != nil {
		return err
	}
	cands := item.Providers
	if len(j.Candidates) > 0 {
		var stored []sku.Provider
		if err := json.Unmarshal(j.Candidates, &stored); err != nil {
			return fmt.Errorf("candidates: %w", err)
		}
		cands = stored
	}
	bj := backend.Job{ID: j.ID, Attempt: 1, Spec: spec, SKU: item, Candidates: cands, Owner: j.Owner}
	atts, err := s.Ledger.Attempts(ctx, j.ID)
	if err != nil {
		return err
	}

	if len(atts) == 0 {
		// Never seen UP: nothing is billed yet and the job may not have
		// reached SkyPilot at all.
		limit, ok, err := s.launchWindow(ctx, j.ID, spec)
		if err != nil {
			return err
		}
		if !ok {
			return s.expire(ctx, j, bj)
		}
		bj.RuntimeLimitSeconds = limit
		if j.Status == store.StatusQueued {
			err = s.Backend.Launch(ctx, bj)
			if errors.Is(err, backend.ErrDuplicate) {
				return nil
			}
			return err
		}
		if err := s.Backend.Resume(ctx, bj, backend.Resumed{}); err != nil {
			return err
		}
	} else {
		a := atts[len(atts)-1]
		bj.Attempt = a.Attempt
		r := backend.Resumed{UpAt: a.StartedAt, LastUpAt: a.UpThrough, Running: j.Status == store.StatusRunning}
		if len(j.Placement) > 0 {
			var p store.Placement
			if err := json.Unmarshal(j.Placement, &p); err == nil {
				r.Placement = &p
			}
		}
		if a.EndedAt != nil {
			r.End = endFromLedger(a, spec)
		}
		if err := s.Backend.Resume(ctx, bj, r); err != nil {
			return err
		}
		if a.EndedAt == nil {
			// Before the first observation reports UP again: the hold has
			// to cover the downtime, or the attempt is capped where it ran
			// out (ledger.Resume).
			if err := s.Ledger.Resume(ctx, j.ID, a.Attempt); err != nil {
				s.log.Warn("resume: hold catch-up", "job", j.ID, "err", err)
			}
			s.restop(ctx, j.ID, a.Attempt)
		}
	}
	s.log.Info("resumed job from an earlier process", "job", j.ID, "status", j.Status, "attempts", len(atts))
	if err := s.Backend.Step(ctx, j.ID); err != nil && !errors.Is(err, backend.ErrUnknownJob) {
		s.log.Warn("resume: first observation", "job", j.ID, "err", err)
	}
	return nil
}

// restop re-issues a stop the ledger decided earlier (H4, H5): the ledger
// asks for a stop once, and the request lived only in the memory of the
// process that died.
func (s *Service) restop(ctx context.Context, jobID string, attempt int) {
	atts, err := s.Ledger.Attempts(ctx, jobID)
	if err != nil {
		return
	}
	for _, a := range atts {
		if a.Attempt != attempt || a.EndedAt != nil || s.now().Before(a.CapAt) {
			continue
		}
		if a.CapReason == ledger.EndBalanceExhausted || a.CapReason == ledger.EndBillingUnavailable {
			s.mu.Lock()
			s.stops[jobID] = a.CapReason
			s.mu.Unlock()
		}
	}
}

// launchWindow is how long a job that was never UP may run if it starts now:
// what max_runtime_seconds has left, and no longer than its hold lasts once
// SettlementReserve is kept back (H6). ok is false when that is below
// MinLaunchRuntime (or the job's own ask, if smaller), or the hold is gone.
func (s *Service) launchWindow(ctx context.Context, jobID string, spec backend.Spec) (limit int, ok bool, err error) {
	h, err := s.Ledger.Hold(ctx, jobID)
	if err != nil {
		return 0, false, err
	}
	u, err := s.Ledger.Usage(ctx, jobID)
	if err != nil {
		return 0, false, err
	}
	remaining := int64(spec.MaxRuntimeSeconds) - u.GPUSeconds
	window := int64(h.ExpiresAt.Add(-SettlementReserve).Sub(s.now()) / time.Second)
	limit64 := min(remaining, window)
	if h.State != "open" || limit64 < min(MinLaunchRuntime, remaining) || limit64 <= 0 {
		return 0, false, nil
	}
	return int(limit64), true, nil
}

// expire cancels a job that waited too long to start. Its hold is released
// in full (B10).
func (s *Service) expire(ctx context.Context, j store.Job, bj backend.Job) error {
	s.log.Warn("resume: job not started within its hold; cancelling", "job", j.ID, "status", j.Status)
	f := &backend.Failure{Code: CodeQueueExpired, Message: queueExpiredMessage}
	if j.Status == store.StatusQueued {
		return s.endUntracked(ctx, j.ID, &store.JobError{Code: f.Code, Message: f.Message})
	}
	// Provisioning: a launch may be in flight. Track it so Stop cancels it
	// and the cluster is torn down.
	if err := s.Backend.Resume(ctx, bj, backend.Resumed{}); err != nil && !errors.Is(err, backend.ErrDuplicate) {
		return err
	}
	err := s.Backend.Stop(ctx, j.ID, s.now(), f)
	if errors.Is(err, backend.ErrAlreadyEnded) {
		return nil
	}
	return err
}

// endFromLedger is the job's end when the ledger already closed the attempt
// for a reason that decides the status alone. A process exit does not: the
// exit code is SkyPilot's, so that attempt is observed like a live one.
func endFromLedger(a ledger.Attempt, spec backend.Spec) *backend.ResumedEnd {
	e := &backend.ResumedEnd{At: *a.EndedAt}
	switch a.EndReason {
	case ledger.EndCancelled:
		e.Reason, e.Status = backend.EndCancelled, store.StatusCancelled
	case ledger.EndBalanceExhausted, ledger.EndBillingUnavailable:
		e.Reason, e.Status = backend.EndCancelled, store.StatusCancelled
		e.Failure = &backend.Failure{Code: string(a.EndReason), Message: stopMessages[a.EndReason]}
	case ledger.EndMaxRuntime:
		e.Reason, e.Status = backend.EndMaxRuntime, store.StatusFailed
		e.Failure = &backend.Failure{Code: backend.CodeMaxRuntimeExceeded,
			Message: fmt.Sprintf("the job ran for its max_runtime_seconds (%d)", spec.MaxRuntimeSeconds)}
	case ledger.EndPreempted:
		e.Reason, e.Status = backend.EndPreempted, store.StatusInterrupted
		e.Failure = &backend.Failure{Code: backend.CodePreempted, Message: "the spot instance was preempted", Retryable: true}
	case ledger.EndProviderFault:
		e.Reason, e.Status = backend.EndProviderFault, store.StatusInterrupted
		e.Failure = &backend.Failure{Code: backend.CodeProviderFault, Message: "the instance stopped responding", Retryable: true}
	default:
		return nil
	}
	return e
}

// carryOutCancels ends every job with a recorded cancel at the time the
// cancel was accepted. A request is dropped once its job is no longer live,
// so a cancel survives the process that accepted it until the job has
// actually ended.
func (s *Service) carryOutCancels(ctx context.Context, active map[string]store.Job) error {
	reqs, err := s.Store.CancelRequests(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range reqs {
		j, live := active[r.JobID]
		if !live {
			errs = append(errs, s.Store.DeleteCancelRequest(ctx, r.JobID))
			continue
		}
		errs = append(errs, s.cancelLive(ctx, j, r.AcceptedAt))
	}
	return errors.Join(errs...)
}

// cancelLive carries out a cancel on a job this process drives. A job that
// is neither tracked nor still queued has an attempt somewhere that has not
// been taken up yet; ending it in the store alone would leave its cluster
// running, so it waits for Reconcile to track it.
func (s *Service) cancelLive(ctx context.Context, j store.Job, acceptedAt time.Time) error {
	if s.Backend != nil && s.Backend.Tracked(j.ID) {
		err := s.Backend.Cancel(context.WithoutCancel(ctx), j.ID, acceptedAt)
		if err != nil && !errors.Is(err, backend.ErrAlreadyEnded) {
			// The end is decided and in the backend's outbox; it becomes
			// durable on a later step.
			s.log.Warn("cancel: recorded, not yet durable", "job", j.ID, "err", err)
		}
		return nil
	}
	if s.Backend == nil || j.Status == store.StatusQueued {
		return s.endUntracked(ctx, j.ID, nil)
	}
	return nil
}

// --- the worker lock ---------------------------------------------------------

// driving is whether this process drives jobs: it holds the worker lock, or
// was built without an election (Config.Elect false).
func (s *Service) driving() bool { return s.drive.Load() }

// Elect tries once for the worker lock and, if it gets it, reconciles before
// returning, so a process that boots as the holder has taken up every live
// job before it serves a submit. Run keeps the lock, or keeps trying for it.
func (s *Service) Elect(ctx context.Context) bool {
	if !s.elect || s.lock != nil {
		return s.lock != nil
	}
	lock, ok, err := s.Store.TryWorkerLock(ctx)
	if err != nil {
		s.log.Warn("worker lock", "err", err)
		return false
	}
	if !ok {
		s.log.Info("worker lock held by another process; serving reads and submits only")
		return false
	}
	s.lock = lock
	s.drive.Store(true)
	s.log.Info("worker lock acquired; driving jobs")
	if err := s.Reconcile(ctx); err != nil {
		s.log.Error("reconcile", "err", err)
	}
	return true
}

// lead runs the workers while the lock holds. It returns when ctx ends or the
// lock is lost, with every worker stopped and nothing tracked, so that no
// step of this process overlaps the next holder's.
func (s *Service) lead(ctx context.Context) {
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runWorkers(wctx)
	}()
	t := time.NewTicker(LockCheck)
	for lost := false; !lost; {
		select {
		case <-ctx.Done():
			lost = true
		case <-t.C:
			if err := s.lock.Check(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("worker lock lost; stopping workers", "err", err)
				lost = true
			}
		}
	}
	t.Stop()
	cancel()
	<-done
	s.stepDown()
}

// stepDown stops driving and forgets everything only a driver knows, then
// gives the lock up.
func (s *Service) stepDown() {
	s.drive.Store(false)
	if s.Backend != nil {
		s.Backend.Forget()
	}
	s.mu.Lock()
	s.stops, s.ended = map[string]ledger.EndReason{}, map[string]backend.Event{}
	s.mu.Unlock()
	s.swept = false
	s.lock.Release()
	s.lock = nil
}

func (s *Service) runElected(ctx context.Context) {
	for {
		if s.lock != nil {
			s.lead(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(LockRetry):
		}
		s.Elect(ctx)
	}
}

// Release gives up the worker lock if this process holds it, for a test that
// stands in for a process exiting without Run.
func (s *Service) Release() {
	if s.lock != nil {
		s.stepDown()
	}
}
