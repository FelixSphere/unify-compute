package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/events"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// backendStore is the backend's store: each status change and placement is
// written in one transaction with the event that announces it.
type backendStore struct{ s *Service }

func (b backendStore) UpdateStatus(ctx context.Context, id string, to store.Status, jobErr *store.JobError) (store.Job, error) {
	return b.s.Store.UpdateStatusWithEvent(ctx, id, to, jobErr, b.s.statusEvent(to))
}

func (b backendStore) SetPlacement(ctx context.Context, id string, p store.Placement) (store.Job, error) {
	return b.s.Store.SetPlacementWithEvent(ctx, id, p, func(store.Job) (*store.NewEvent, error) {
		return newEvent(events.JobPlaced, events.PlacedData{Placement: &events.Placement{
			Cloud: p.Cloud, Region: p.Region, InstanceType: p.InstanceType}})
	})
}

// statusEvent is the event a transition to `to` emits (events.md, "Event
// types"); the four terminal statuses map one-to-one onto the four terminal
// events.
func (s *Service) statusEvent(to store.Status) func(store.Job) (*store.NewEvent, error) {
	return func(j store.Job) (*store.NewEvent, error) {
		switch to {
		case store.StatusProvisioning:
			return newEvent(events.JobProvisioning, nil)
		case store.StatusRunning:
			return newEvent(events.JobRunning, nil)
		case store.StatusSucceeded:
			return newEvent(events.JobSucceeded, nil)
		case store.StatusFailed:
			d := events.FailedData{Error: store.JobError{Code: "failed"}}
			if j.Error != nil {
				d.Error = *j.Error
			}
			if e, ok := s.endedEdge(j.ID); ok && e.Failure != nil {
				d.Retryable = e.Failure.Retryable
			}
			return newEvent(events.JobFailed, d)
		case store.StatusInterrupted:
			provider := "unknown"
			if e, ok := s.endedEdge(j.ID); ok && e.Placement != nil && e.Placement.Cloud != "" {
				provider = e.Placement.Cloud
			}
			return newEvent(events.JobPreempted, events.PreemptedData{Provider: provider})
		case store.StatusCancelled:
			reason := "customer"
			if j.Error != nil && (j.Error.Code == string(ledger.EndBalanceExhausted) || j.Error.Code == string(ledger.EndBillingUnavailable)) {
				reason = j.Error.Code
			}
			return newEvent(events.JobCancelled, events.CancelledData{Reason: reason})
		}
		return nil, nil
	}
}

func (s *Service) endedEdge(jobID string) (backend.Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.ended[jobID]
	return e, ok
}

// sink turns the backend's lifecycle edges into ledger calls. An error from
// it holds the backend at that edge and the same edge is emitted again, so
// every call here is one the ledger treats as idempotent.
type sink struct{ s *Service }

var _ backend.UpObserver = sink{}

func (k sink) Emit(ctx context.Context, e backend.Event) error {
	l := k.s.Ledger
	switch e.Kind {
	case backend.EventClusterUp:
		// The GPU meter starts here, and the job does not read running
		// until the ledger has the attempt (backend.md, "Durability").
		return l.Started(ctx, e.JobID, e.Attempt, e.At)
	case backend.EventJobStarted:
		return l.Up(ctx, e.JobID, e.Attempt, e.ObservedAt)
	case backend.EventEnded:
		k.s.mu.Lock()
		k.s.ended[e.JobID] = e
		k.s.mu.Unlock()
		if e.UpAt != nil {
			var err error
			switch e.Reason {
			case backend.EndPreempted:
				err = l.Interrupted(ctx, e.JobID, e.Attempt, ledger.EndPreempted, e.At)
			case backend.EndProviderFault:
				err = l.Interrupted(ctx, e.JobID, e.Attempt, ledger.EndProviderFault, e.At)
			case backend.EndCancelled:
				err = l.Stopped(ctx, e.JobID, e.Attempt, e.At, ledger.EndCancelled)
			case backend.EndMaxRuntime:
				err = l.Stopped(ctx, e.JobID, e.Attempt, e.At, ledger.EndMaxRuntime)
			default:
				err = l.Stopped(ctx, e.JobID, e.Attempt, e.At, ledger.EndExited)
			}
			if err != nil {
				return err
			}
		}
		// Retry is not built, so an ended attempt is the job's end.
		return k.s.finish(ctx, e.JobID)
	case backend.EventClusterDown:
		k.s.mu.Lock()
		delete(k.s.ended, e.JobID)
		k.s.mu.Unlock()
	}
	return nil
}

func (k sink) ObservedUp(ctx context.Context, jobID string, attempt int, at time.Time) error {
	return k.s.Ledger.Up(ctx, jobID, attempt, at)
}

// actions is what the ledger asks of the rest of compute-api. Both methods
// are called with the ledger's lock held, so neither may call back into it.
type actions struct{ s *Service }

func (a actions) Stop(_ context.Context, jobID string, reason ledger.EndReason) error {
	if reason == ledger.EndMaxRuntime {
		// The backend ends the attempt at exactly the same cap on its own
		// (backend.md, "Max runtime"), as failed/max_runtime_exceeded.
		return nil
	}
	a.s.mu.Lock()
	a.s.stops[jobID] = reason
	a.s.mu.Unlock()
	return nil
}

func (a actions) Notify(ctx context.Context, jobID, event string) error {
	switch event {
	case "extend_refused":
		ne, err := newEvent(events.BalanceExhausted, nil)
		if err != nil {
			return err
		}
		ne.JobID = jobID
		_, err = a.s.Store.AppendEvent(ctx, *ne)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	default:
		a.s.log.Warn("ledger notice", "job", jobID, "event", event)
	}
	return nil
}
