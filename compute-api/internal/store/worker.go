package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// workerLockClass is the first key of the worker lock's two-key advisory
// lock. The second is hashtext(current_schema()), so the lock is per set of
// tables: one writer per schema, which is what the ledger and the backend
// assume, and test schemas in one database do not contend. Two-key and
// one-key advisory locks never conflict, so migrationLockID is unaffected.
const workerLockClass = 0x636f6d70

// WorkerLock is the session-level advisory lock held by the one compute-api
// process that drives jobs: the backend poll loop, the ledger tick, and
// carrying out cancels (backend.md, "Restart"). Postgres releases it when the
// holding session ends, including when the process dies without closing it.
type WorkerLock struct {
	conn *pgxpool.Conn
}

// TryWorkerLock takes the worker lock if no other session holds it. ok is
// false, with no error, when another process has it.
func (s *Store) TryWorkerLock(ctx context.Context) (lock *WorkerLock, ok bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext(current_schema()))`, int32(workerLockClass)).Scan(&ok)
	if err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return &WorkerLock{conn: conn}, true, nil
}

// Check reports whether the session holding the lock is still alive and
// still holds it. A false answer or an error means another process may
// already be driving jobs, so the holder must stop at once.
func (l *WorkerLock) Check(ctx context.Context) error {
	var held bool
	err := l.conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted
			  AND classid = $1::int::oid AND objid = hashtext(current_schema())::oid AND objsubid = 2)`,
		int32(workerLockClass)).Scan(&held)
	if err != nil {
		return fmt.Errorf("store: worker lock: %w", err)
	}
	if !held {
		return errors.New("store: worker lock no longer held")
	}
	return nil
}

// Release gives the lock up and returns the connection. A broken connection
// is closed instead, which ends the session and releases the lock with it.
func (l *WorkerLock) Release() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1, hashtext(current_schema()))`, int32(workerLockClass)); err != nil {
		_ = l.conn.Conn().Close(ctx)
	}
	l.conn.Release()
}

// CancelRequest is a customer cancel waiting for the worker-lock holder.
type CancelRequest struct {
	JobID      string
	AcceptedAt time.Time
}

// RequestCancel records a cancel accepted at acceptedAt for the lock holder
// to carry out. A job already waiting keeps its first acceptance time: that
// is when billing stops.
func (s *Store) RequestCancel(ctx context.Context, jobID string, acceptedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO job_cancel_requests (job_id, accepted_at) VALUES ($1, $2)
		ON CONFLICT (job_id) DO NOTHING`, jobID, acceptedAt)
	return err
}

// CancelRequests lists pending cancels, oldest first.
func (s *Store) CancelRequests(ctx context.Context) ([]CancelRequest, error) {
	rows, err := s.pool.Query(ctx, `SELECT job_id, accepted_at FROM job_cancel_requests ORDER BY created_at, job_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CancelRequest, error) {
		var c CancelRequest
		err := r.Scan(&c.JobID, &c.AcceptedAt)
		return c, err
	})
}

// DeleteCancelRequest removes a cancel once it has been carried out.
func (s *Store) DeleteCancelRequest(ctx context.Context, jobID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM job_cancel_requests WHERE job_id = $1`, jobID)
	return err
}
