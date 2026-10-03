package ledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

var (
	ErrUnknownJob = errors.New("ledger: no hold for job")
	// ErrRuntimeExhausted refuses an attempt that would start with
	// max_runtime_seconds already billed (I5).
	ErrRuntimeExhausted = errors.New("ledger: max_runtime_seconds already billed")
	ErrAttemptOrder     = errors.New("ledger: attempt started while an earlier one is still open")
)

// InsufficientBalanceError is a refused hold at submit (H2). It carries both
// the amount needed now and what the whole job can cost.
type InsufficientBalanceError struct {
	HoldQuota    int64
	CeilingQuota int64
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("ledger: hold of %d refused (job ceiling %d)", e.HoldQuota, e.CeilingQuota)
}

// Job is what the ledger needs at submit. RateUSDPerHour is the SKU's price
// snapshotted then; a later price change never touches the job.
type Job struct {
	JobID             string
	UserID            int
	TokenID           int
	SKU               string
	RateUSDPerHour    string
	MaxRuntimeSeconds int64
	SubmittedAt       time.Time
}

// Hold is the ledger's view of a job's console hold. Quotas are console
// quota units.
type Hold struct {
	HoldID          string
	JobID           string
	State           string
	OriginalQuota   int64
	CurrentQuota    int64
	CeilingQuota    int64
	SettledQuota    int64
	UnbilledQuota   int64
	CancelCode      EndReason
	PendingExtendID string
	ExpiresAt       time.Time
}

type UsageEvent struct {
	ID                 string
	JobID              string
	Meter              Meter
	Attempt            int
	From, To           time.Time
	Quantity           int64
	Rate               string
	Amount             int64
	CumulativeQuantity int64
	CumulativeAmount   int64
	Final              bool
}

// Engine is the GPU meter's ledger and settlement loop. It assumes it is the
// only writer of its tables: one compute-api process per database. Running
// several needs a per-job lease, which does not exist yet.
type Engine struct {
	db      *pgxpool.Pool
	console *console.Client
	actions Actions
	now     func() time.Time
	mu      sync.Mutex
}

func New(db *pgxpool.Pool, c *console.Client, a Actions, now func() time.Time) *Engine {
	if now == nil {
		now = time.Now
	}
	return &Engine{db: db, console: c, actions: a, now: now}
}

func (e *Engine) clock() time.Time { return e.now().Truncate(time.Millisecond) }

// quota is metering.md's quota(s): one half-up rounding on the exact product.
func quota(rate string, seconds int64) int64 {
	q, err := sku.SKU{PriceUSDPerHour: rate}.EstimateQuota(seconds)
	if err != nil {
		// Rates are validated before a hold row exists, so a bad one here
		// means the database was edited by hand.
		panic("ledger: invalid stored rate " + rate)
	}
	return q
}

// billed is floor(end − start) in seconds, both at millisecond resolution.
func billed(start, end time.Time) int64 {
	if !end.After(start) {
		return 0
	}
	return int64(end.Truncate(time.Millisecond).Sub(start.Truncate(time.Millisecond)) / time.Second)
}

// --- hold ----------------------------------------------------------------

// PlaceHold places the H1 hold for a job about to be submitted. It is safe
// to retry with the same Job: the hold id is persisted before the console is
// called, so a retry after a lost response is the same request and the
// console reserves once.
func (e *Engine) PlaceHold(ctx context.Context, j Job) (Hold, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j.MaxRuntimeSeconds <= 0 {
		return Hold{}, errors.New("ledger: max_runtime_seconds must be positive")
	}
	if _, err := (sku.SKU{PriceUSDPerHour: j.RateUSDPerHour}).EstimateQuota(1); err != nil {
		return Hold{}, fmt.Errorf("ledger: rate: %w", err)
	}
	initial := quota(j.RateUSDPerHour, min(j.MaxRuntimeSeconds, InitialHoldSeconds))
	if initial <= 0 {
		return Hold{}, errors.New("ledger: hold rounds to zero quota")
	}
	ceiling := quota(j.RateUSDPerHour, j.MaxRuntimeSeconds)
	expires := j.SubmittedAt.Truncate(time.Second).Add(time.Duration(j.MaxRuntimeSeconds)*time.Second + HoldGrace)

	_, err := e.db.Exec(ctx, `
		INSERT INTO holds (hold_id, job_id, user_id, token_id, sku, rate, max_runtime_seconds,
			original_quota, expires_at, ceiling_quota, current_quota)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 0)
		ON CONFLICT (job_id) DO NOTHING`,
		store.NewID("hold_", e.now()), j.JobID, j.UserID, j.TokenID, j.SKU, j.RateUSDPerHour,
		j.MaxRuntimeSeconds, initial, expires, ceiling)
	if err != nil {
		return Hold{}, fmt.Errorf("ledger: insert hold: %w", err)
	}
	h, err := loadHold(ctx, e.db, j.JobID)
	if err != nil {
		return Hold{}, err
	}
	switch h.State {
	case "pending":
	case "refused":
		return Hold{}, &InsufficientBalanceError{HoldQuota: h.OriginalQuota, CeilingQuota: h.CeilingQuota}
	default:
		return h.public(), nil
	}
	_, err = e.console.Hold(ctx, console.HoldRequest{
		HoldID: h.HoldID, UserID: h.UserID, TokenID: h.TokenID, Quota: h.OriginalQuota,
		JobID: h.JobID, SKU: h.SKU, ExpiresAt: h.ExpiresAt.Unix(),
	})
	switch classify(err) {
	case outcomeOK:
		h.State, h.CurrentQuota = "open", h.OriginalQuota
	case outcomeRefused:
		h.State = "refused"
	default:
		return Hold{}, fmt.Errorf("ledger: place hold: %w", err)
	}
	if err := saveHold(ctx, e.db, h); err != nil {
		return Hold{}, err
	}
	if h.State == "refused" {
		return Hold{}, &InsufficientBalanceError{HoldQuota: h.OriginalQuota, CeilingQuota: h.CeilingQuota}
	}
	return h.public(), nil
}

// --- lifecycle -----------------------------------------------------------

func (e *Engine) Started(ctx context.Context, jobID string, attempt int, at time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	at = at.Truncate(time.Millisecond)
	return pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		h, err := loadHold(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if h.State != "open" && h.State != "expired" {
			return fmt.Errorf("ledger: job %s: hold is %s", jobID, h.State)
		}
		atts, err := loadAttempts(ctx, tx, jobID)
		if err != nil {
			return err
		}
		for _, a := range atts {
			if a.Attempt == attempt {
				return nil
			}
			if a.EndedAt == nil {
				return ErrAttemptOrder
			}
		}
		last, err := latestEvent(ctx, tx, jobID)
		if err != nil {
			return err
		}
		remaining := h.MaxRuntimeSeconds - last.CumulativeQuantity
		if remaining <= 0 {
			return ErrRuntimeExhausted
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO metering_attempts (job_id, attempt, started_at, up_through, cap_at, cap_reason)
			VALUES ($1, $2, $3, $3, $4, $5)`,
			jobID, attempt, at, at.Add(time.Duration(remaining)*time.Second), string(EndMaxRuntime))
		return err
	})
}

func (e *Engine) Up(ctx context.Context, jobID string, attempt int, at time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	at = at.Truncate(time.Millisecond)
	return e.withAttempt(ctx, jobID, attempt, func(tx pgx.Tx, h *holdRow, a *attemptRow) error {
		if !at.After(a.UpThrough) {
			return nil
		}
		if !at.Before(a.CapAt) {
			return endAttempt(ctx, tx, h, a, a.CapAt, EndReason(a.CapReason))
		}
		_, err := tx.Exec(ctx, `UPDATE metering_attempts SET up_through = $3 WHERE job_id = $1 AND attempt = $2`, jobID, attempt, at)
		return err
	})
}

func (e *Engine) Stopped(ctx context.Context, jobID string, attempt int, at time.Time, reason EndReason) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	at = at.Truncate(time.Millisecond)
	return e.withAttempt(ctx, jobID, attempt, func(tx pgx.Tx, h *holdRow, a *attemptRow) error {
		if !at.Before(a.CapAt) {
			at, reason = a.CapAt, EndReason(a.CapReason)
		}
		return endAttempt(ctx, tx, h, a, at, reason)
	})
}

func (e *Engine) Interrupted(ctx context.Context, jobID string, attempt int, reason EndReason, reported time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.withAttempt(ctx, jobID, attempt, func(tx pgx.Tx, h *holdRow, a *attemptRow) error {
		// I3: without a provider time, the last observation of UP, never
		// the moment we noticed the cluster was gone.
		end := a.UpThrough
		if !reported.IsZero() {
			end = reported.Truncate(time.Millisecond)
		}
		if !end.Before(a.CapAt) {
			end, reason = a.CapAt, EndReason(a.CapReason)
		}
		return endAttempt(ctx, tx, h, a, end, reason)
	})
}

// Finished closes metering for the job and settles it. An attempt still open
// is ended at its last observed UP: the adapter should have reported how it
// ended, and in its absence the customer is not billed for time nobody saw.
func (e *Engine) Finished(ctx context.Context, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		h, err := loadHold(ctx, tx, jobID)
		if err != nil {
			return err
		}
		switch h.State {
		case "open", "expired":
		case "pending":
			return fmt.Errorf("ledger: job %s: hold never confirmed", jobID)
		default:
			return nil
		}
		atts, err := loadAttempts(ctx, tx, jobID)
		if err != nil {
			return err
		}
		for i := range atts {
			a := &atts[i]
			if a.EndedAt != nil {
				continue
			}
			reason := EndExited
			if a.StopRequestedAt != nil {
				reason = EndReason(a.CapReason)
			}
			if err := endAttempt(ctx, tx, &h, a, a.UpThrough, reason); err != nil {
				return err
			}
		}
		last, err := latestEvent(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if last.ID != "" {
			// A fresh id for the final settle: re-sending an id the console
			// may already hold as non-final would replay that result and
			// leave the hold open.
			if _, err := writeEvent(ctx, tx, &h, last.Attempt, last.To, last.To, 0, true); err != nil {
				return err
			}
		}
		if h.State == "expired" {
			h.State = "closed"
			h.UnbilledQuota = max(0, last.CumulativeAmount-h.SettledQuota)
		} else {
			h.State = "closing"
		}
		return saveHold(ctx, tx, h)
	})
	if err != nil {
		return err
	}
	return e.step(ctx, jobID)
}

func (e *Engine) withAttempt(ctx context.Context, jobID string, attempt int, fn func(pgx.Tx, *holdRow, *attemptRow) error) error {
	return pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		h, err := loadHold(ctx, tx, jobID)
		if err != nil {
			return err
		}
		atts, err := loadAttempts(ctx, tx, jobID)
		if err != nil {
			return err
		}
		for i := range atts {
			if atts[i].Attempt == attempt {
				if atts[i].EndedAt != nil {
					return nil
				}
				return fn(tx, &h, &atts[i])
			}
		}
		return fmt.Errorf("ledger: job %s has no attempt %d", jobID, attempt)
	})
}

// endAttempt writes the attempt's remaining usage, interval by interval so
// the 5-minute structure survives, then the partial interval up to end.
func endAttempt(ctx context.Context, tx pgx.Tx, h *holdRow, a *attemptRow, end time.Time, reason EndReason) error {
	last, err := meterTo(ctx, tx, h, a, end)
	if err != nil {
		return err
	}
	if end.After(last) {
		q := billed(a.StartedAt, end) - billed(a.StartedAt, last)
		if _, err := writeEvent(ctx, tx, h, a.Attempt, last, end, q, false); err != nil {
			return err
		}
	} else {
		end = last
	}
	_, err = tx.Exec(ctx, `
		UPDATE metering_attempts SET ended_at = $3, end_reason = $4, up_through = GREATEST(up_through, $3)
		WHERE job_id = $1 AND attempt = $2`, a.JobID, a.Attempt, end, string(reason))
	return err
}

// meterTo writes one event per settlement boundary (R1: every 300 s from the
// attempt's UP) up to through, and returns how far the attempt is metered.
func meterTo(ctx context.Context, tx pgx.Tx, h *holdRow, a *attemptRow, through time.Time) (time.Time, error) {
	last, err := meteredThrough(ctx, tx, a)
	if err != nil {
		return time.Time{}, err
	}
	if through.After(a.CapAt) {
		through = a.CapAt
	}
	k := last.Sub(a.StartedAt)/SettleInterval + 1
	for b := a.StartedAt.Add(k * SettleInterval); !b.After(through); b = b.Add(SettleInterval) {
		q := billed(a.StartedAt, b) - billed(a.StartedAt, last)
		if _, err := writeEvent(ctx, tx, h, a.Attempt, last, b, q, false); err != nil {
			return time.Time{}, err
		}
		last = b
	}
	return last, nil
}

// --- settlement loop -----------------------------------------------------

// Tick advances every job with a live hold: writes usage due, settles it,
// keeps the hold ahead, and stops jobs the hold can no longer cover. Call it
// more often than SettleInterval; between boundaries it costs a few reads.
// Console failures are state, not errors: they are retried on the next tick.
func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, err := e.db.Query(ctx, `SELECT job_id FROM holds WHERE state IN ('open', 'closing', 'expired') ORDER BY created_at`)
	if err != nil {
		return err
	}
	jobs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range jobs {
		if err := e.step(ctx, j); err != nil {
			errs = append(errs, fmt.Errorf("job %s: %w", j, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) step(ctx context.Context, jobID string) error {
	now := e.clock()
	var h holdRow
	var latest, open *attemptRow
	err := pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		var err error
		if h, err = loadHold(ctx, tx, jobID); err != nil {
			return err
		}
		atts, err := loadAttempts(ctx, tx, jobID)
		if err != nil || len(atts) == 0 {
			return err
		}
		latest = &atts[len(atts)-1]
		if latest.EndedAt == nil {
			open = latest
			_, err = meterTo(ctx, tx, &h, open, open.UpThrough)
		}
		return err
	})
	if err != nil {
		return err
	}

	var errs []error
	if latest != nil && (h.State == "open" || h.State == "expired") {
		errs = append(errs, e.requestStop(ctx, &h, latest, now))
	}
	switch h.State {
	case "closing":
		return errors.Join(append(errs, e.close(ctx, &h))...)
	case "open":
	default:
		return errors.Join(errs...)
	}
	// Between attempts, or after the last one ended, nothing accrues: the
	// tail of the ended attempt waits for the final settle (H7) or the next
	// attempt's first one, and the hold needs no runway (B4).
	if open == nil {
		return errors.Join(errs...)
	}

	tried, err := e.settle(ctx, &h)
	errs = append(errs, err)
	if h.State == "open" && h.CancelCode == "" &&
		(tried || (h.PendingExtendID != nil && h.LastFailure == "unavailable")) {
		errs = append(errs, e.extend(ctx, &h))
	}
	if h.State == "open" {
		errs = append(errs, e.cancelIfUncovered(ctx, &h, open, now))
	}
	return errors.Join(errs...)
}

// settle sends the newest unsettled event. Earlier unsettled ones need no
// call of their own: amounts are cumulative, so the newest supersedes them
// (R4).
func (e *Engine) settle(ctx context.Context, h *holdRow) (bool, error) {
	ev, err := latestEvent(ctx, e.db, h.JobID)
	if err != nil || ev.ID == "" || (h.SettledEventID != nil && *h.SettledEventID == ev.ID) {
		return false, err
	}
	if ev.CumulativeAmount > h.CurrentQuota {
		// The console would answer hold_exceeded. Mid-run the extend that
		// follows can fix it; at the end nothing can, and H4/H5 exist so
		// this never happens, so say so loudly rather than retry forever.
		if ev.Final {
			return true, fmt.Errorf("ledger: final usage %d exceeds hold %d", ev.CumulativeAmount, h.CurrentQuota)
		}
		return true, nil
	}
	_, err = e.console.Settle(ctx, h.HoldID, console.SettleRequest{
		EventID: ev.ID, CumulativeQuota: ev.CumulativeAmount, GPUSeconds: ev.CumulativeQuantity,
		SKU: h.SKU, Final: ev.Final,
	})
	return true, e.record(ctx, h, err, func() {
		h.SettledQuota, h.SettledEventID = ev.CumulativeAmount, &ev.ID
		if h.LastFailure == "unavailable" {
			h.LastFailure = ""
		}
		if ev.Final {
			h.State = "closed"
		}
	})
}

// close finishes a terminal job's hold: a final settle when there is usage
// the console has not confirmed, otherwise a release, so a job that never ran
// leaves no consume-log row (B8, B10).
func (e *Engine) close(ctx context.Context, h *holdRow) error {
	ev, err := latestEvent(ctx, e.db, h.JobID)
	if err != nil {
		return err
	}
	if ev.ID != "" && ev.CumulativeAmount != h.SettledQuota {
		if !ev.Final {
			return errors.New("ledger: closing hold without a final event")
		}
		_, err = e.settle(ctx, h)
	} else {
		_, cerr := e.console.Release(ctx, h.HoldID)
		err = e.record(ctx, h, cerr, func() { h.State = "closed" })
	}
	if h.State == "expired" {
		h.State, h.UnbilledQuota = "closed", max(0, ev.CumulativeAmount-h.SettledQuota)
		err = errors.Join(err, saveHold(ctx, e.db, *h))
	}
	return err
}

// extend keeps at least Runway of rate ahead of settled usage (H3). The
// extend id is persisted before the call and reused until the console
// answers, so a lost response cannot reserve twice.
func (e *Engine) extend(ctx context.Context, h *holdRow) error {
	runway := quota(h.Rate, int64(Runway/time.Second))
	if h.CurrentQuota >= h.CeilingQuota || (h.PendingExtendID == nil && h.CurrentQuota-h.SettledQuota >= runway) {
		return nil
	}
	if h.PendingExtendID == nil {
		id, amount := store.NewID("ext_", e.now()), min(runway, h.CeilingQuota-h.CurrentQuota)
		h.PendingExtendID, h.PendingExtendQuota = &id, &amount
		if err := saveHold(ctx, e.db, *h); err != nil {
			return err
		}
	}
	res, err := e.console.Extend(ctx, h.HoldID, console.ExtendRequest{ExtendID: *h.PendingExtendID, AdditionalQuota: *h.PendingExtendQuota})
	firstRefusal := false
	err = e.record(ctx, h, err, func() {
		h.CurrentQuota = res.Quota
		h.PendingExtendID, h.PendingExtendQuota = nil, nil
		h.ExtendRefusedAt, h.LastFailure = nil, ""
	}, func() {
		if h.ExtendRefusedAt == nil {
			now := e.now()
			h.ExtendRefusedAt, firstRefusal = &now, true
		}
	})
	if firstRefusal && e.actions != nil {
		err = errors.Join(err, e.actions.Notify(ctx, h.JobID, "extend_refused"))
	}
	return err
}

// cancelIfUncovered stops a job whose hold has less than CancelMargin of
// rate left over what it has accrued (H4, H5). Accrued is reckoned at now,
// not at the last observation, so the decision errs towards stopping.
func (e *Engine) cancelIfUncovered(ctx context.Context, h *holdRow, a *attemptRow, now time.Time) error {
	if h.CancelCode != "" || h.CurrentQuota >= h.CeilingQuota {
		return nil
	}
	err := pgx.BeginFunc(ctx, e.db, func(tx pgx.Tx) error {
		last, err := latestEvent(ctx, tx, h.JobID)
		if err != nil {
			return err
		}
		through, err := meteredThrough(ctx, tx, a)
		if err != nil {
			return err
		}
		at := now
		if at.After(a.CapAt) {
			at = a.CapAt
		}
		seconds := last.CumulativeQuantity + billed(a.StartedAt, at) - billed(a.StartedAt, through)
		if h.CurrentQuota-quota(h.Rate, seconds) >= quota(h.Rate, int64(CancelMargin/time.Second)) {
			return nil
		}
		code := EndBillingUnavailable
		if h.LastFailure == "refused" {
			code = EndBalanceExhausted
		}
		h.CancelCode = code
		if err := saveHold(ctx, tx, *h); err != nil {
			return err
		}
		if at.Before(a.CapAt) {
			a.CapAt, a.CapReason = at, string(code)
		}
		_, err = tx.Exec(ctx, `UPDATE metering_attempts SET cap_at = $3, cap_reason = $4 WHERE job_id = $1 AND attempt = $2`,
			a.JobID, a.Attempt, a.CapAt, a.CapReason)
		return err
	})
	if err != nil {
		return err
	}
	return e.requestStop(ctx, h, a, now)
}

// requestStop asks the backend to stop a job whose metering cap has passed,
// once; a failed request is retried on the next tick. An attempt that ended
// for another reason (exit, customer cancel) before its cap needs no stop.
func (e *Engine) requestStop(ctx context.Context, h *holdRow, a *attemptRow, now time.Time) error {
	if a.StopRequestedAt != nil || now.Before(a.CapAt) || e.actions == nil ||
		(a.EndReason != nil && *a.EndReason != a.CapReason) {
		return nil
	}
	if err := e.actions.Stop(ctx, h.JobID, EndReason(a.CapReason)); err != nil {
		return err
	}
	a.StopRequestedAt = &now
	_, err := e.db.Exec(ctx, `UPDATE metering_attempts SET stop_requested_at = $3 WHERE job_id = $1 AND attempt = $2`, a.JobID, a.Attempt, now)
	return err
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeRefused
	outcomeGone
	outcomeExceeded
	outcomeUnavailable
)

// classify maps a bridge result onto what the engine does next. Anything it
// cannot act on (transport errors, 5xx, 429, a dark bridge, a signature the
// console rejects) is "unavailable": usage keeps accruing in the ledger and
// the call is retried, and if it lasts the hold runs out and H5 applies.
func classify(err error) outcome {
	if err == nil {
		return outcomeOK
	}
	var ce *console.Error
	if !errors.As(err, &ce) {
		return outcomeUnavailable
	}
	switch {
	case ce.Status == http.StatusPaymentRequired:
		return outcomeRefused
	case errors.Is(err, console.ErrHoldNotFound),
		ce.Status == http.StatusConflict && (ce.Message == "hold_expired" || ce.Message == "hold_closed"):
		return outcomeGone
	case ce.Status == http.StatusConflict && ce.Message == "hold_exceeded":
		return outcomeExceeded
	}
	return outcomeUnavailable
}

// record applies a console result to the hold and saves it. onRefused is
// optional.
func (e *Engine) record(ctx context.Context, h *holdRow, err error, onOK func(), onRefused ...func()) error {
	var notify bool
	switch classify(err) {
	case outcomeOK:
		onOK()
		err = nil
	case outcomeRefused:
		h.LastFailure = "refused"
		for _, f := range onRefused {
			f()
		}
		err = nil
	case outcomeGone:
		// R6: the console swept the hold. Metering goes on in the ledger;
		// what cannot be settled is recorded as unbilled at the end, to be
		// charged by the daily path once it exists.
		notify = h.State == "open" || h.State == "closing"
		h.State = "expired"
		err = nil
	case outcomeExceeded:
		err = nil
	case outcomeUnavailable:
		h.LastFailure = "unavailable"
		var ce *console.Error
		if !errors.As(err, &ce) || ce.Status >= 500 || ce.Status == http.StatusTooManyRequests {
			err = nil
		}
	}
	if serr := saveHold(ctx, e.db, *h); serr != nil {
		return serr
	}
	if notify && e.actions != nil {
		return e.actions.Notify(ctx, h.JobID, "hold_expired")
	}
	return err
}

// --- reads ---------------------------------------------------------------

func (e *Engine) Hold(ctx context.Context, jobID string) (Hold, error) {
	h, err := loadHold(ctx, e.db, jobID)
	return h.public(), err
}

func (e *Engine) Events(ctx context.Context, jobID string) ([]UsageEvent, error) {
	rows, err := e.db.Query(ctx, `SELECT `+eventColumns+` FROM usage_events WHERE job_id = $1 ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEvent)
}
