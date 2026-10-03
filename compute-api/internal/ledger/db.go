package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type holdRow struct {
	HoldID             string
	JobID              string
	UserID             int
	TokenID            int
	SKU                string
	Rate               string
	MaxRuntimeSeconds  int64
	OriginalQuota      int64
	ExpiresAt          time.Time
	CeilingQuota       int64
	CurrentQuota       int64
	State              string
	SettledQuota       int64
	SettledEventID     *string
	PendingExtendID    *string
	PendingExtendQuota *int64
	ExtendRefusedAt    *time.Time
	LastFailure        string
	CancelCode         EndReason
	UnbilledQuota      int64
}

func (h holdRow) public() Hold {
	p := Hold{
		HoldID: h.HoldID, JobID: h.JobID, State: h.State,
		OriginalQuota: h.OriginalQuota, CurrentQuota: h.CurrentQuota, CeilingQuota: h.CeilingQuota,
		SettledQuota: h.SettledQuota, UnbilledQuota: h.UnbilledQuota, CancelCode: h.CancelCode,
		ExpiresAt: h.ExpiresAt,
	}
	if h.PendingExtendID != nil {
		p.PendingExtendID = *h.PendingExtendID
	}
	return p
}

func loadHold(ctx context.Context, q querier, jobID string) (holdRow, error) {
	var h holdRow
	var lastFailure, cancelCode *string
	err := q.QueryRow(ctx, `
		SELECT hold_id, job_id, user_id, token_id, sku, rate, max_runtime_seconds, original_quota,
			expires_at, ceiling_quota, current_quota, state, settled_quota, settled_event_id,
			pending_extend_id, pending_extend_quota, extend_refused_at, last_failure, cancel_code,
			unbilled_quota
		FROM holds WHERE job_id = $1`, jobID).Scan(
		&h.HoldID, &h.JobID, &h.UserID, &h.TokenID, &h.SKU, &h.Rate, &h.MaxRuntimeSeconds, &h.OriginalQuota,
		&h.ExpiresAt, &h.CeilingQuota, &h.CurrentQuota, &h.State, &h.SettledQuota, &h.SettledEventID,
		&h.PendingExtendID, &h.PendingExtendQuota, &h.ExtendRefusedAt, &lastFailure, &cancelCode,
		&h.UnbilledQuota)
	if errors.Is(err, pgx.ErrNoRows) {
		return holdRow{}, fmt.Errorf("%w %s", ErrUnknownJob, jobID)
	}
	if lastFailure != nil {
		h.LastFailure = *lastFailure
	}
	if cancelCode != nil {
		h.CancelCode = EndReason(*cancelCode)
	}
	return h, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func saveHold(ctx context.Context, q querier, h holdRow) error {
	_, err := q.Exec(ctx, `
		UPDATE holds SET current_quota = $2, state = $3, settled_quota = $4, settled_event_id = $5,
			pending_extend_id = $6, pending_extend_quota = $7, extend_refused_at = $8,
			last_failure = $9, cancel_code = $10, unbilled_quota = $11, updated_at = now()
		WHERE hold_id = $1`,
		h.HoldID, h.CurrentQuota, h.State, h.SettledQuota, h.SettledEventID,
		h.PendingExtendID, h.PendingExtendQuota, h.ExtendRefusedAt,
		nullable(h.LastFailure), nullable(string(h.CancelCode)), h.UnbilledQuota)
	if err != nil {
		return fmt.Errorf("ledger: save hold: %w", err)
	}
	return nil
}

type attemptRow struct {
	JobID           string
	Attempt         int
	StartedAt       time.Time
	UpThrough       time.Time
	CapAt           time.Time
	CapReason       string
	StopRequestedAt *time.Time
	EndedAt         *time.Time
	EndReason       *string
}

func loadAttempts(ctx context.Context, q querier, jobID string) ([]attemptRow, error) {
	rows, err := q.Query(ctx, `
		SELECT job_id, attempt, started_at, up_through, cap_at, cap_reason, stop_requested_at, ended_at, end_reason
		FROM metering_attempts WHERE job_id = $1 ORDER BY attempt`, jobID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (attemptRow, error) {
		var a attemptRow
		err := r.Scan(&a.JobID, &a.Attempt, &a.StartedAt, &a.UpThrough, &a.CapAt, &a.CapReason,
			&a.StopRequestedAt, &a.EndedAt, &a.EndReason)
		return a, err
	})
}

const eventColumns = `id, job_id, meter, coalesce(attempt, 0), from_at, to_at, quantity, rate,
	coalesce(amount, 0), coalesce(cumulative_quantity, 0), coalesce(cumulative_amount, 0), final`

func scanEvent(r pgx.CollectableRow) (UsageEvent, error) {
	var ev UsageEvent
	var meter string
	err := r.Scan(&ev.ID, &ev.JobID, &meter, &ev.Attempt, &ev.From, &ev.To, &ev.Quantity, &ev.Rate,
		&ev.Amount, &ev.CumulativeQuantity, &ev.CumulativeAmount, &ev.Final)
	ev.Meter = Meter(meter)
	return ev, err
}

// latestEvent is the job's newest GPU event, or a zero event when there is
// none. Its cumulative fields are the job's running totals.
func latestEvent(ctx context.Context, q querier, jobID string) (UsageEvent, error) {
	rows, err := q.Query(ctx, `SELECT `+eventColumns+` FROM usage_events
		WHERE job_id = $1 AND meter = 'gpu_seconds' ORDER BY seq DESC LIMIT 1`, jobID)
	if err != nil {
		return UsageEvent{}, err
	}
	evs, err := pgx.CollectRows(rows, scanEvent)
	if err != nil || len(evs) == 0 {
		return UsageEvent{}, err
	}
	return evs[0], nil
}

// meteredThrough is read from the ledger itself, so the ledger alone says
// how far an attempt has been billed.
func meteredThrough(ctx context.Context, q querier, a *attemptRow) (time.Time, error) {
	var t *time.Time
	err := q.QueryRow(ctx, `SELECT max(to_at) FROM usage_events WHERE job_id = $1 AND meter = 'gpu_seconds' AND attempt = $2`,
		a.JobID, a.Attempt).Scan(&t)
	if err != nil || t == nil {
		return a.StartedAt, err
	}
	return *t, nil
}

// writeEvent appends one GPU usage event. The cumulative quota is computed
// on the cumulative seconds, so rounding happens once however the seconds
// are split into intervals and attempts.
func writeEvent(ctx context.Context, q querier, h *holdRow, attempt int, from, to time.Time, seconds int64, final bool) (UsageEvent, error) {
	prev, err := latestEvent(ctx, q, h.JobID)
	if err != nil {
		return UsageEvent{}, err
	}
	cumQ := prev.CumulativeQuantity + seconds
	cumA := quota(h.Rate, cumQ)
	ev := UsageEvent{
		ID: store.NewID("ue_", time.Now()), JobID: h.JobID, Meter: MeterGPU, Attempt: attempt,
		From: from, To: to, Quantity: seconds, Rate: h.Rate,
		Amount: cumA - prev.CumulativeAmount, CumulativeQuantity: cumQ, CumulativeAmount: cumA, Final: final,
	}
	_, err = q.Exec(ctx, `
		INSERT INTO usage_events (id, job_id, meter, attempt, from_at, to_at, quantity, rate,
			cumulative_quantity, cumulative_amount, amount, final)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		ev.ID, ev.JobID, string(ev.Meter), ev.Attempt, ev.From, ev.To, ev.Quantity, ev.Rate,
		ev.CumulativeQuantity, ev.CumulativeAmount, ev.Amount, ev.Final)
	if err != nil {
		return UsageEvent{}, fmt.Errorf("ledger: append usage event: %w", err)
	}
	return ev, nil
}
