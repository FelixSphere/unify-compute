package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Status string

const (
	StatusQueued       Status = "queued"
	StatusProvisioning Status = "provisioning"
	StatusRunning      Status = "running"
	StatusSucceeded    Status = "succeeded"
	StatusFailed       Status = "failed"
	StatusInterrupted  Status = "interrupted"
	StatusCancelled    Status = "cancelled"
)

// transitions is the state diagram in docs/spec/job-api.md, as revised on
// 2026-10-01 to let a job fail or be interrupted before it starts (image pull
// failure, capacity exhausted everywhere, preemption during provisioning).
// Anything not listed is refused, including a status to itself, so a caller
// that re-reports the current state learns it raced rather than silently
// rewriting timestamps.
var transitions = map[Status][]Status{
	StatusQueued:       {StatusProvisioning, StatusCancelled},
	StatusProvisioning: {StatusRunning, StatusFailed, StatusInterrupted, StatusCancelled},
	StatusRunning:      {StatusSucceeded, StatusFailed, StatusInterrupted, StatusCancelled},
}

func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusProvisioning, StatusRunning,
		StatusSucceeded, StatusFailed, StatusInterrupted, StatusCancelled:
		return true
	}
	return false
}

func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusInterrupted, StatusCancelled:
		return true
	}
	return false
}

// CanTransition reports whether the state diagram allows from → to.
func CanTransition(from, to Status) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

func sourcesOf(to Status) []string {
	var out []string
	for from, tos := range transitions {
		for _, t := range tos {
			if t == to {
				out = append(out, string(from))
			}
		}
	}
	return out
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Job struct {
	ID        string
	Owner     string
	TokenID   *int64
	SKU       string
	Status    Status
	Spec      json.RawMessage
	Metadata  map[string]string
	Error     *JobError
	Placement json.RawMessage
	// Candidates is where the job may be placed, as resolved at submit (a
	// JSON array of sku.Provider); nil for a job stored without them.
	Candidates json.RawMessage
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	UpdatedAt  time.Time
}

// NewJob is what a submit persists. Validation of the spec, SKU and metadata
// against the API contract is the handler's job; the store only enforces what
// the schema can (a JSON object spec, a non-empty owner and SKU).
type NewJob struct {
	// ID is the job's id when the caller needs it before the row exists (the
	// console hold names the job); empty means the store picks one.
	ID       string
	Owner    string
	TokenID  *int64
	SKU      string
	Spec     json.RawMessage
	Metadata map[string]string
	// Candidates is kept so a job relaunched by another process goes where
	// this submit allowed (backend.md, "Restart"). Nil stores nothing.
	Candidates json.RawMessage
	// Events are appended in the insert's transaction, so a job never
	// exists without its job.queued event. JobID is filled in.
	Events []NewEvent
}

// Idempotency carries a submit's Idempotency-Key. RequestHash is the caller's
// digest of the request body; the store compares it byte for byte and does
// not interpret it.
type Idempotency struct {
	Key         string
	RequestHash []byte
}

// IdempotencyRetention is how long a key binds to its job (job-api.md: 24h).
// A key older than this is treated as unused even before it is purged.
const IdempotencyRetention = 24 * time.Hour

const jobColumns = `id, owner, token_id, sku, status, spec, metadata, error, placement, candidates,
	created_at, started_at, finished_at, updated_at`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	var status string
	var meta, jobErr []byte
	err := row.Scan(&j.ID, &j.Owner, &j.TokenID, &j.SKU, &status, &j.Spec, &meta, &jobErr, &j.Placement, &j.Candidates,
		&j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.UpdatedAt)
	if err != nil {
		return Job{}, err
	}
	j.Status = Status(status)
	if err := json.Unmarshal(meta, &j.Metadata); err != nil {
		return Job{}, fmt.Errorf("store: job %s metadata: %w", j.ID, err)
	}
	if jobErr != nil {
		j.Error = new(JobError)
		if err := json.Unmarshal(jobErr, j.Error); err != nil {
			return Job{}, fmt.Errorf("store: job %s error: %w", j.ID, err)
		}
	}
	return j, nil
}

// Create persists a queued job. With idem set, a key this owner used within
// IdempotencyRetention returns that original job and created=false if the
// request hash matches, and ErrIdempotencyConflict if it does not. Concurrent
// submits with one key produce one job: the second blocks on the key's row
// until the first commits, then replays it.
func (s *Store) Create(ctx context.Context, nj NewJob, idem *Idempotency) (job Job, created bool, err error) {
	if idem != nil && (len(idem.Key) == 0 || len([]rune(idem.Key)) > 255) {
		return Job{}, false, ErrInvalidIdempotencyKey
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback(ctx)

	job, replay, err := insertJob(ctx, tx, nj, idem)
	if err != nil {
		return Job{}, false, err
	}
	if replay {
		_ = tx.Rollback(ctx)
		return s.replay(ctx, nj.Owner, idem)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

// insertJob inserts nj and binds idem to it inside tx. replay=true means the
// key is already bound to a live job; the caller must roll back and replay.
func insertJob(ctx context.Context, tx pgx.Tx, nj NewJob, idem *Idempotency) (job Job, replay bool, err error) {
	meta := nj.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return Job{}, false, err
	}

	id := nj.ID
	if id == "" {
		id = NewJobID(time.Now())
	}
	job, err = scanJob(tx.QueryRow(ctx, `
		INSERT INTO jobs (id, owner, token_id, sku, spec, metadata, candidates)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+jobColumns,
		id, nj.Owner, nj.TokenID, nj.SKU, []byte(nj.Spec), metaJSON, nullJSON(nj.Candidates)))
	if err != nil {
		return Job{}, false, fmt.Errorf("store: insert job: %w", err)
	}

	if idem != nil {
		// The DO UPDATE only fires for an expired key, so a stale row that
		// the purge has not reached yet is reclaimed instead of replayed.
		var bound string
		err = tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys (owner, key, request_hash, job_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (owner, key) DO UPDATE
				SET request_hash = EXCLUDED.request_hash, job_id = EXCLUDED.job_id, created_at = now()
				WHERE idempotency_keys.created_at <= now() - make_interval(secs => $5)
			RETURNING job_id`,
			nj.Owner, idem.Key, idem.RequestHash, job.ID, IdempotencyRetention.Seconds()).Scan(&bound)
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, true, nil
		}
		if err != nil {
			return Job{}, false, fmt.Errorf("store: bind idempotency key: %w", err)
		}
	}
	for _, ne := range nj.Events {
		ne.JobID = job.ID
		if _, err := appendEventTx(ctx, tx, ne); err != nil {
			return Job{}, false, err
		}
	}
	return job, false, nil
}

func nullJSON(b json.RawMessage) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (s *Store) replay(ctx context.Context, owner string, idem *Idempotency) (Job, bool, error) {
	var hash []byte
	var jobID string
	err := s.pool.QueryRow(ctx,
		`SELECT request_hash, job_id FROM idempotency_keys WHERE owner = $1 AND key = $2`,
		owner, idem.Key).Scan(&hash, &jobID)
	if err != nil {
		return Job{}, false, fmt.Errorf("store: read idempotency key: %w", err)
	}
	if string(hash) != string(idem.RequestHash) {
		return Job{}, false, ErrIdempotencyConflict
	}
	job, err := s.Get(ctx, owner, jobID)
	return job, false, err
}

// Get returns the job only if owner owns it. Another owner's job and a job
// that does not exist are the same ErrNotFound.
func (s *Store) Get(ctx context.Context, owner, id string) (Job, error) {
	job, err := scanJob(s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE owner = $1 AND id = $2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

type ListParams struct {
	Owner    string
	Statuses []Status // any of; empty means all
	After    string   // job id from a previous page's Next
	Limit    int      // <= 0 means DefaultListLimit; capped at MaxListLimit
}

type Page struct {
	Jobs    []Job
	HasMore bool
	Next    string // id to pass as After for the following page; empty when !HasMore
}

// List returns owner's jobs newest first. The cursor is keyset, not offset,
// so a job created between pages neither repeats nor shifts later pages.
func (s *Store) List(ctx context.Context, p ListParams) (Page, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	statuses := make([]string, 0, len(p.Statuses))
	for _, st := range p.Statuses {
		if !st.Valid() {
			return Page{}, fmt.Errorf("%w: %q", ErrInvalidStatus, st)
		}
		statuses = append(statuses, string(st))
	}

	var cursorAt *time.Time
	if p.After != "" {
		var at time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at FROM jobs WHERE owner = $1 AND id = $2`, p.Owner, p.After).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return Page{}, ErrInvalidCursor
		}
		if err != nil {
			return Page{}, err
		}
		cursorAt = &at
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE owner = $1
		  AND (cardinality($2::text[]) = 0 OR status = ANY($2))
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC
		LIMIT $5`,
		p.Owner, statuses, cursorAt, p.After, limit+1)
	if err != nil {
		return Page{}, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Job, error) { return scanJob(r) })
	if err != nil {
		return Page{}, err
	}
	page := Page{Jobs: jobs}
	if len(jobs) > limit {
		page.Jobs = jobs[:limit]
		page.HasMore = true
		page.Next = page.Jobs[limit-1].ID
	}
	return page, nil
}

// UpdateStatus moves a job along the state diagram. It is not owner-scoped:
// its callers are the backend and the metering loop, which act on a job id
// they already hold. started_at is stamped on entering running and
// finished_at on entering a terminal state. jobErr, when non-nil, replaces
// the job's error.
//
// The check and the write are one statement, so two racing updates cannot
// both succeed from the same source state.
func (s *Store) UpdateStatus(ctx context.Context, id string, to Status, jobErr *JobError) (Job, error) {
	return updateStatus(ctx, s.pool, id, to, jobErr)
}

// UpdateStatusWithEvent is UpdateStatus and the event that announces it, in
// one transaction (docs/spec/events.md): a crash between the two cannot leave
// a terminal job without its terminal webhook, and a refused transition
// writes no event. event builds the event from the updated job; nil from it
// means none.
func (s *Store) UpdateStatusWithEvent(ctx context.Context, id string, to Status, jobErr *JobError, event func(Job) (*NewEvent, error)) (Job, error) {
	var job Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if job, err = updateStatus(ctx, tx, id, to, jobErr); err != nil {
			return err
		}
		return appendBuilt(ctx, tx, job, event)
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func appendBuilt(ctx context.Context, tx pgx.Tx, job Job, event func(Job) (*NewEvent, error)) error {
	if event == nil {
		return nil
	}
	ne, err := event(job)
	if err != nil || ne == nil {
		return err
	}
	ne.JobID = job.ID
	_, err = appendEventTx(ctx, tx, *ne)
	return err
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func updateStatus(ctx context.Context, q rowQuerier, id string, to Status, jobErr *JobError) (Job, error) {
	if !to.Valid() {
		return Job{}, fmt.Errorf("%w: %q", ErrInvalidStatus, to)
	}
	var errJSON []byte
	if jobErr != nil {
		b, err := json.Marshal(jobErr)
		if err != nil {
			return Job{}, err
		}
		errJSON = b
	}
	job, err := scanJob(q.QueryRow(ctx, `
		UPDATE jobs SET
			status      = $2,
			error       = COALESCE($3::jsonb, error),
			started_at  = CASE WHEN $2 = 'running' THEN now() ELSE started_at END,
			finished_at = CASE WHEN $4 THEN now() ELSE NULL END,
			updated_at  = now()
		WHERE id = $1 AND status = ANY($5)
		RETURNING `+jobColumns,
		id, string(to), errJSON, to.Terminal(), sourcesOf(to)))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Job{}, err
	}
	var from string
	err = q.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&from)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return Job{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
}

// ActiveJobs returns every job not yet in a terminal state, across owners,
// oldest first. The caller is the process itself at boot, not a customer.
func (s *Store) ActiveJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE status IN ('queued', 'provisioning', 'running')
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Job, error) { return scanJob(r) })
}

// RecentlyFinished returns every job that reached a terminal state within
// the last `within`, across owners. The caller is the process itself, looking
// for clusters an earlier process may have left up.
func (s *Store) RecentlyFinished(ctx context.Context, within time.Duration) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+jobColumns+` FROM jobs
		WHERE finished_at > now() - make_interval(secs => $1)
		ORDER BY finished_at, id`, within.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Job, error) { return scanJob(r) })
}

// PurgeExpiredIdempotencyKeys deletes keys older than IdempotencyRetention
// and returns how many went. Expired keys are already ignored by Create, so
// this only bounds the table; how often it runs does not affect correctness.
func (s *Store) PurgeExpiredIdempotencyKeys(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE created_at <= now() - make_interval(secs => $1)`,
		IdempotencyRetention.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
