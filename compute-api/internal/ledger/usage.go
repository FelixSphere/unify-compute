package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// JobUsage is what the Job object's `usage` is rendered from. Quotas are
// console quota units.
type JobUsage struct {
	GPUSeconds        int64
	AccruedQuota      int64
	SettledQuota      int64
	HoldQuota         int64 // the live hold; 0 once it is closed
	RateUSDPerHour    string
	MaxRuntimeSeconds int64
	State             string
}

// Usage reads a job's metering totals and hold. It never calls the console.
func (e *Engine) Usage(ctx context.Context, jobID string) (JobUsage, error) {
	h, err := loadHold(ctx, e.db, jobID)
	if err != nil {
		return JobUsage{}, err
	}
	last, err := latestEvent(ctx, e.db, jobID)
	if err != nil {
		return JobUsage{}, err
	}
	u := JobUsage{
		GPUSeconds: last.CumulativeQuantity, AccruedQuota: last.CumulativeAmount, SettledQuota: h.SettledQuota,
		RateUSDPerHour: h.Rate, MaxRuntimeSeconds: h.MaxRuntimeSeconds, State: h.State,
	}
	switch h.State {
	case "open", "closing", "expired":
		u.HoldQuota = h.CurrentQuota
	}
	return u, nil
}

// Abandon gives back the hold of a job that will never run: the submit that
// placed it failed before the job was persisted, or the hold's outcome is
// unknown because the console's answer was lost. The hold goes to closing and
// is released on this call or a later Tick, so a lost response cannot leave
// the customer's balance locked until the hold expires. A refused hold
// reserved nothing and is left as it is.
func (e *Engine) Abandon(ctx context.Context, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		h, err := loadHold(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if h.State != "pending" && h.State != "open" {
			return nil
		}
		atts, err := loadAttempts(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if len(atts) > 0 {
			return fmt.Errorf("ledger: job %s has metered attempts; finish it instead", jobID)
		}
		h.State = "closing"
		return saveHold(ctx, tx, h)
	})
	if err != nil {
		return err
	}
	return e.step(ctx, jobID)
}
