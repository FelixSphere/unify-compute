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

// platformLockClass/platformLockKey name the one advisory lock every
// platform-limited submit takes before its owner's lock. It is a different
// class from admissionLockClass, so no owner's hashtext can collide with it.
const (
	platformLockClass = 0x706c6174 // "plat"
	platformLockKey   = 0
)

const platformUsageSQL = `
	SELECT sku, count(*)::int, (count(*) FILTER (WHERE status = 'queued'))::int
	FROM jobs
	WHERE status IN ('queued', 'provisioning', 'running')
	GROUP BY sku
	ORDER BY sku`

const activeUsageSQL = `
	SELECT sku, count(*)::int, (count(*) FILTER (WHERE status = 'queued'))::int
	FROM jobs
	WHERE owner = $1 AND status IN ('queued', 'provisioning', 'running')
	GROUP BY sku
	ORDER BY sku`

// ActiveUsage returns owner's active jobs per SKU. The status predicate is
// jobs_active_idx's, so the planner can scan active jobs only, a set the
// quotas themselves keep small, instead of the owner's whole history.
func (s *Store) ActiveUsage(ctx context.Context, owner string) ([]SKUUsage, error) {
	return activeUsage(ctx, s.pool, owner)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func activeUsage(ctx context.Context, q querier, owner string) ([]SKUUsage, error) {
	return collectUsage(q.Query(ctx, activeUsageSQL, owner))
}

func collectUsage(rows pgx.Rows, err error) ([]SKUUsage, error) {
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
	return s.createAdmitted(ctx, nj, idem, false, func(ctx context.Context, tx pgx.Tx) error {
		usage, err := activeUsage(ctx, tx, nj.Owner)
		if err != nil {
			return fmt.Errorf("store: active usage: %w", err)
		}
		return admit(ctx, usage)
	})
}

// PlatformAdmitFunc is AdmitFunc with the usage of every owner as well.
type PlatformAdmitFunc func(ctx context.Context, owner, platform []SKUUsage) error

// CreateAdmittedPlatform is CreateAdmitted for a deployment with
// platform-wide limits. Before the owner's lock it takes one advisory lock
// shared by every such submit, so the platform-wide usage admit sees cannot
// change until this transaction ends: two owners cannot both take the
// platform's last slot. The price is that all submits are serialised across
// the console hold, which at one active job platform-wide is no limit at all.
//
// Lock order is always platform, then owner. CreateAdmitted takes only the
// owner's, and never waits on the platform lock while holding it, so the two
// can run side by side without deadlock.
func (s *Store) CreateAdmittedPlatform(ctx context.Context, nj NewJob, idem *Idempotency, admit PlatformAdmitFunc) (job Job, created bool, err error) {
	return s.createAdmitted(ctx, nj, idem, true, func(ctx context.Context, tx pgx.Tx) error {
		usage, err := activeUsage(ctx, tx, nj.Owner)
		if err != nil {
			return fmt.Errorf("store: active usage: %w", err)
		}
		platform, err := collectUsage(tx.Query(ctx, platformUsageSQL))
		if err != nil {
			return fmt.Errorf("store: platform usage: %w", err)
		}
		return admit(ctx, usage, platform)
	})
}

func (s *Store) createAdmitted(ctx context.Context, nj NewJob, idem *Idempotency, platform bool, admit func(context.Context, pgx.Tx) error) (job Job, created bool, err error) {
	if idem != nil && (len(idem.Key) == 0 || len([]rune(idem.Key)) > 255) {
		return Job{}, false, ErrInvalidIdempotencyKey
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback(ctx)

	if platform {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int4, $2::int4)`, platformLockClass, platformLockKey); err != nil {
			return Job{}, false, fmt.Errorf("store: platform admission lock: %w", err)
		}
	}
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

	if err := admit(ctx, tx); err != nil {
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
