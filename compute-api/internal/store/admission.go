package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SKUUsage is how many of an owner's active jobs (queued, provisioning,
// running) are on one SKU. GPUs and tiers are the catalogue's business, so the
// store reports SKUs and the quota package maps them.
type SKUUsage struct {
	SKU    string
	Active int
	Queued int
}

// admissionLockClass is the first key of the two-key advisory lock taken per
// owner by CreateAdmitted; the second is hashtext(owner). Two-key locks do not
// share a keyspace with the single-key migration lock. A hash collision only
// makes two owners' submits wait for each other.
const admissionLockClass = 0x71756f74 // "quot"

const activeUsageSQL = `
	SELECT sku, count(*)::int, (count(*) FILTER (WHERE status = 'queued'))::int
	FROM jobs
	WHERE owner = $1 AND status IN ('queued', 'provisioning', 'running')
	GROUP BY sku
	ORDER BY sku`

// ActiveUsage returns owner's active jobs per SKU. The status predicate
// matches jobs_active_idx, so the scan is over active jobs only, which the
// quotas themselves keep small.
func (s *Store) ActiveUsage(ctx context.Context, owner string) ([]SKUUsage, error) {
	return activeUsage(ctx, s.pool, owner)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func activeUsage(ctx context.Context, q querier, owner string) ([]SKUUsage, error) {
	rows, err := q.Query(ctx, activeUsageSQL, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SKUUsage, error) {
		var u SKUUsage
		err := r.Scan(&u.SKU, &u.Active, &u.Queued)
		return u, err
	})
}

// AdmitFunc decides whether a submit may proceed given the owner's active
// usage at the moment of the decision. A non-nil error refuses the submit and
// is returned unchanged from CreateAdmitted. It is also where the caller
// places the console hold, after its own checks pass: the hold then happens
// only for a submit that will be persisted unless the hold itself fails.
type AdmitFunc func(ctx context.Context, usage []SKUUsage) error

// CreateAdmitted is Create with an admission decision that cannot race. It
// takes a transaction-scoped advisory lock on the owner, so concurrent submits
// for one owner run one at a time from the usage read to the commit: the
// second sees the first's job, and two submits cannot both take the last
// slot. Other owners are not blocked.
//
// An Idempotency-Key already bound to a live job replays that job without
// calling admit, so retrying a submit that succeeded never reports the
// owner's quota as exceeded by the job the retry is asking about.
//
// admit runs while the lock and a pooled connection are held, so it must be
// bounded in time; the console client's timeout is that bound.
func (s *Store) CreateAdmitted(ctx context.Context, nj NewJob, idem *Idempotency, admit AdmitFunc) (job Job, created bool, err error) {
	if idem != nil && (len(idem.Key) == 0 || len([]rune(idem.Key)) > 255) {
		return Job{}, false, ErrInvalidIdempotencyKey
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, hashtext($2))`, admissionLockClass, nj.Owner); err != nil {
		return Job{}, false, fmt.Errorf("store: admission lock: %w", err)
	}

	if idem != nil {
		var bound string
		err := tx.QueryRow(ctx, `
			SELECT job_id FROM idempotency_keys
			WHERE owner = $1 AND key = $2 AND created_at > now() - make_interval(secs => $3)`,
			nj.Owner, idem.Key, IdempotencyRetention.Seconds()).Scan(&bound)
		switch {
		case err == nil:
			_ = tx.Rollback(ctx)
			return s.replay(ctx, nj.Owner, idem)
		case !errors.Is(err, pgx.ErrNoRows):
			return Job{}, false, fmt.Errorf("store: read idempotency key: %w", err)
		}
	}

	usage, err := activeUsage(ctx, tx, nj.Owner)
	if err != nil {
		return Job{}, false, fmt.Errorf("store: active usage: %w", err)
	}
	if err := admit(ctx, usage); err != nil {
		return Job{}, false, err
	}

	job, replay, err := insertJob(ctx, tx, nj, idem)
	if err != nil {
		return Job{}, false, err
	}
	if replay {
		// Unreachable while every submit for this owner goes through the
		// lock; a plain Create racing us is the only way here.
		_ = tx.Rollback(ctx)
		return s.replay(ctx, nj.Owner, idem)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}
