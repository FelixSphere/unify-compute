package backend

import (
	"context"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Resumed is what outlived the process that launched an attempt, enough to
// track it again (backend.md, "Restart"). The cluster name is derived from
// the job id and attempt; the rest comes from the ledger and the store.
type Resumed struct {
	// UpAt is the attempt's cluster_up time (the ledger's started_at) and
	// LastUpAt the last observation of UP the ledger recorded (up_through).
	// Zero UpAt: the attempt was never seen UP.
	UpAt, LastUpAt time.Time
	// Placement is the job's stored placement, nil if none was written.
	Placement *Placement
	// Running is whether the store already reads running. If it does not,
	// the cluster_up edge's store write is redone on the next UP.
	Running bool
	// End, when set, is an end the ledger already recorded with a reason
	// that decides the job's status by itself (anything but a process exit,
	// whose exit code only SkyPilot knows). The attempt is ended with it at
	// once and torn down.
	End *ResumedEnd
}

type ResumedEnd struct {
	Reason  EndReason
	At      time.Time
	Status  store.Status
	Failure *Failure
}

// Resume tracks an attempt this process did not launch. It asks SkyPilot
// only for the attempt's launch request, so that an in-flight launch is
// adopted rather than sent twice; what happened to the cluster is learnt on
// the next Step, by the same observations as any attempt. An error leaves
// nothing tracked, so the caller can try again.
//
// A job still queued has no attempt to resume: Launch it.
func (b *Backend) Resume(ctx context.Context, j Job, r Resumed) error {
	if j.Attempt < 1 {
		j.Attempt = 1
	}
	task, err := TaskFor(j, b.cfg.Task)
	if err != nil {
		return err
	}
	cluster := ClusterName(j.ID, j.Attempt)
	info, found, err := b.api.LaunchRequestFor(ctx, cluster)
	if err != nil {
		return err
	}

	b.mu.Lock()
	if _, ok := b.attempts[j.ID]; ok {
		b.mu.Unlock()
		return ErrDuplicate
	}
	a := &attempt{job: j, cluster: cluster, task: task}
	a.mu.Lock()
	defer a.mu.Unlock()
	b.attempts[j.ID] = a
	b.mu.Unlock()

	if r.UpAt.IsZero() {
		// Provisioning when the last process stopped. Without a launch
		// request SkyPilot never got the POST, or lost its request table
		// in a redeploy: submit adopts or launches on the next Step, under
		// the same cluster name.
		a.phase = phaseSubmit
		if found {
			a.launchReq, a.launchFinished, a.phase = info.ID, info.Status.Finished(), phaseLaunching
		}
	} else {
		a.phase, a.resumed, a.started = phaseUp, true, true
		a.upAt, a.lastUpAt = r.UpAt, r.LastUpAt
		if a.lastUpAt.Before(a.upAt) {
			a.lastUpAt = a.upAt
		}
		a.pendingRunning = !r.Running
		if r.Placement != nil {
			p := *r.Placement
			a.placement = &p
		}
		if found {
			a.launchReq = info.ID
		} else {
			// The request log is gone; the cluster queue is read without
			// a job id, which pickJob resolves to the newest (the cluster
			// runs only ours).
			a.launchDone, a.launchFinished = true, true
		}
	}
	if r.End != nil {
		b.end(ctx, a, r.End.Reason, r.End.At, r.End.Status, r.End.Failure)
		if err := b.flush(ctx, a); err != nil {
			b.logf("backend: %s: resumed end: %v (retried on next step)", j.ID, err)
		}
	}
	return nil
}

// Tracked reports whether this backend is driving the job's attempt.
func (b *Backend) Tracked(jobID string) bool {
	_, ok := b.get(jobID)
	return ok
}

// Forget drops every tracked attempt without touching SkyPilot. A process
// that loses the worker lock calls it: another process may now drive those
// attempts, and if this one gets the lock back it resumes them from durable
// state like any other. An edge still in an outbox is decided again by the
// next holder: from SkyPilot, from the ledger's record of the attempt, or,
// for a customer cancel, from the cancel request the store keeps until the
// job has ended.
func (b *Backend) Forget() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attempts = map[string]*attempt{}
}

// TearDownLeftovers tears down the clusters of jobs that already ended but
// whose teardown the previous process may not have finished: the end is
// durable before /down is sent, so a process that dies in between leaves a
// cluster running, and after a cancel the customer's job keeps it busy, so
// SkyPilot's idle autodown never fires. One /status call covers every job;
// each cluster still there is tracked in teardown until SkyPilot confirms it
// down. Nothing is emitted for its end, which was emitted already.
func (b *Backend) TearDownLeftovers(ctx context.Context, js []Job) (int, error) {
	byCluster := map[string]Job{}
	var names []string
	for _, j := range js {
		if j.Attempt < 1 {
			j.Attempt = 1
		}
		if b.Tracked(j.ID) {
			continue
		}
		c := ClusterName(j.ID, j.Attempt)
		byCluster[c] = j
		names = append(names, c)
	}
	if len(names) == 0 {
		return 0, nil
	}
	recs, err := b.api.Status(ctx, names...)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range recs {
		j, ok := byCluster[r.Name]
		if !ok {
			continue
		}
		b.mu.Lock()
		if _, tracked := b.attempts[j.ID]; !tracked {
			b.attempts[j.ID] = &attempt{job: j, cluster: r.Name, phase: phaseTeardown,
				ended: &Event{ID: eventID(j.ID, j.Attempt, EventEnded), JobID: j.ID, Attempt: j.Attempt, Kind: EventEnded}}
			n++
		}
		b.mu.Unlock()
	}
	return n, nil
}
