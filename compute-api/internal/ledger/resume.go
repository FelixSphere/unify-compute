package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Attempt is the ledger's durable record of one attempt (one cluster
// lifetime). It is what a restarted compute-api rebuilds the backend's view
// from: StartedAt is the attempt's UP time, UpThrough the last time it was
// observed UP with the job still the customer's (I3).
type Attempt struct {
	Attempt   int
	StartedAt time.Time
	UpThrough time.Time
	CapAt     time.Time
	CapReason EndReason
	EndedAt   *time.Time
	EndReason EndReason
}

// Attempts returns the job's metered attempts, oldest first. A job that never
// reached UP has none. ErrUnknownJob means no hold exists for the job.
func (e *Engine) Attempts(ctx context.Context, jobID string) ([]Attempt, error) {
	if _, err := loadHold(ctx, e.db, jobID); err != nil {
		return nil, err
	}
	rows, err := loadAttempts(ctx, e.db, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]Attempt, len(rows))
	for i, a := range rows {
		out[i] = Attempt{Attempt: a.Attempt, StartedAt: a.StartedAt, UpThrough: a.UpThrough, CapAt: a.CapAt,
			CapReason: EndReason(a.CapReason), EndedAt: a.EndedAt}
		if a.EndReason != nil {
			out[i].EndReason = EndReason(*a.EndReason)
		}
	}
	return out, nil
}

// Resume prepares an open attempt for metering to continue after compute-api
// was not running. Nothing ran the settlement loop meanwhile, so nothing kept
// the hold ahead of usage (H3), and if the cluster stayed UP the downtime is
// the customer's (metering.md, "Restart"). Before the attempt is reported UP
// again, the hold is extended to cover usage accrued up to now plus Runway,
// in one extend. If the console cannot or will not cover it, the attempt is
// capped where the hold ran out by CancelMargin — the point H4/H5 would have
// stopped it at had compute-api been running — and stopped from there. So a
// downtime the hold did not cover is never billed past the hold, and the
// final settle always fits inside it.
//
// It never moves up_through: whether the downtime is billed at all is decided
// by the next observation of the cluster, not here.
func (e *Engine) Resume(ctx context.Context, jobID string, attempt int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clock()
	h, err := loadHold(ctx, e.db, jobID)
	if err != nil {
		return err
	}
	if h.State != "open" || h.CancelCode != "" {
		return nil
	}
	a, err := openAttempt(ctx, e.db, jobID, attempt)
	if err != nil || a == nil {
		return err
	}
	at := now
	if at.After(a.CapAt) {
		at = a.CapAt
	}
	base, err := accruedBase(ctx, e.db, a)
	if err != nil {
		return err
	}
	accrued := base + billed(a.StartedAt, at)
	target := min(h.CeilingQuota, quota(h.Rate, accrued)+quota(h.Rate, int64(Runway/time.Second)))

	// Two rounds: an extend the previous process left pending is re-sent
	// first with its own id and amount (the console dedupes on it), and may
	// not be enough on its own.
	var errs []error
	firstRefusal := false
	for range 2 {
		if h.CurrentQuota >= target && h.PendingExtendID == nil {
			break
		}
		if h.PendingExtendID == nil {
			id, amount := store.NewID("ext_", e.now()), target-h.CurrentQuota
			h.PendingExtendID, h.PendingExtendQuota = &id, &amount
			if err := saveHold(ctx, e.db, h); err != nil {
				return err
			}
		}
		res, err := e.console.Extend(ctx, h.HoldID, console.ExtendRequest{ExtendID: *h.PendingExtendID, AdditionalQuota: *h.PendingExtendQuota})
		ok := classify(err) == outcomeOK
		errs = append(errs, e.record(ctx, &h, err, func() {
			h.CurrentQuota = res.Quota
			h.PendingExtendID, h.PendingExtendQuota = nil, nil
			h.ExtendRefusedAt, h.LastFailure = nil, ""
		}, func() {
			if h.ExtendRefusedAt == nil {
				t := e.now()
				h.ExtendRefusedAt, firstRefusal = &t, true
			}
		}))
		if !ok {
			break
		}
	}
	if h.State != "open" || h.CurrentQuota >= h.CeilingQuota {
		return errors.Join(errs...)
	}
	margin := quota(h.Rate, int64(CancelMargin/time.Second))
	if h.CurrentQuota-quota(h.Rate, accrued) >= margin {
		return errors.Join(errs...)
	}

	// The latest whole second of this attempt the hold covers with the
	// margin left, never before what was already observed.
	budget := h.CurrentQuota - margin
	lo, hi := billed(a.StartedAt, a.UpThrough), billed(a.StartedAt, at)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if quota(h.Rate, base+mid) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	capAt := a.StartedAt.Add(time.Duration(lo) * time.Second)
	if capAt.Before(a.UpThrough) {
		capAt = a.UpThrough
	}
	code := EndBillingUnavailable
	if h.LastFailure == "refused" {
		code = EndBalanceExhausted
	}
	err = pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		h.CancelCode = code
		if err := saveHold(ctx, tx, h); err != nil {
			return err
		}
		a.CapAt, a.CapReason = capAt, string(code)
		_, err := tx.Exec(ctx, `UPDATE metering_attempts SET cap_at = $3, cap_reason = $4 WHERE job_id = $1 AND attempt = $2`,
			a.JobID, a.Attempt, a.CapAt, a.CapReason)
		return err
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if firstRefusal && e.actions != nil {
		errs = append(errs, e.actions.Notify(ctx, h.JobID, "extend_refused"))
	}
	return errors.Join(append(errs, e.requestStop(ctx, &h, a, now))...)
}

func openAttempt(ctx context.Context, q querier, jobID string, attempt int) (*attemptRow, error) {
	atts, err := loadAttempts(ctx, q, jobID)
	if err != nil {
		return nil, err
	}
	for i := range atts {
		if atts[i].Attempt == attempt {
			if atts[i].EndedAt != nil {
				return nil, nil
			}
			return &atts[i], nil
		}
	}
	return nil, fmt.Errorf("ledger: job %s has no attempt %d", jobID, attempt)
}

// accruedBase is the job's cumulative billed seconds minus what this attempt
// has contributed so far, so base + billed(StartedAt, t) is the cumulative
// total at t, the way cancelIfUncovered reckons it.
func accruedBase(ctx context.Context, q querier, a *attemptRow) (int64, error) {
	last, err := latestEvent(ctx, q, a.JobID)
	if err != nil {
		return 0, err
	}
	through, err := meteredThrough(ctx, q, a)
	if err != nil {
		return 0, err
	}
	return last.CumulativeQuantity - billed(a.StartedAt, through), nil
}
