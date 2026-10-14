package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RequestOwnerCancelsTx records a cancel, accepted at acceptedAt, for every
// live job of owner, inside the caller's transaction, and returns their ids.
// It is RequestCancel for a whole account: the request is durable with the
// caller's commit, and the worker-lock holder's Reconcile carries it out
// even if nobody calls Cancel afterwards.
func RequestOwnerCancelsTx(ctx context.Context, tx pgx.Tx, owner string, acceptedAt time.Time) ([]string, error) {
	rows, err := tx.Query(ctx, `
		INSERT INTO job_cancel_requests (job_id, accepted_at)
		SELECT id, $2 FROM jobs WHERE owner = $1 AND status IN ('queued', 'provisioning', 'running')
		ON CONFLICT (job_id) DO NOTHING
		RETURNING job_id`, owner, acceptedAt)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
