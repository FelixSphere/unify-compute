package billing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The dashboard's reads of the balance history and of monthly usage
// (accounts.md, "GET /transactions" and "GET /usage"). Both read only what
// the verbs above wrote; neither takes a lock.

// ErrInvalidCursor is an `after` that is not an entry of this account.
var ErrInvalidCursor = errors.New("billing: cursor is not an entry of this account")

// HistoryEntry is a balance entry as the account holder may see it: the
// movement, and the job it belongs to when it belongs to one. The ref of an
// operator adjustment (an audit event id) and its detail (the operator's
// reason) are deliberately not part of it.
type HistoryEntry struct {
	ID                        string
	Kind                      string
	AvailableDelta, HeldDelta int64
	AvailableAfter, HeldAfter int64
	RefType, RefID            string
	// JobID and SKU come from the hold a hold, extend, settle or release
	// entry moved; empty for every other kind.
	JobID, SKU string
	CreatedAt  time.Time
}

type HistoryPage struct {
	Entries []HistoryEntry
	HasMore bool
}

// History lists an account's entries newest first, strictly older than the
// entry named by after (empty for the newest).
func (l *Local) History(ctx context.Context, accountID int64, after string, limit int) (HistoryPage, error) {
	var before *int64
	if after != "" {
		var seq int64
		err := l.db.QueryRow(ctx, `SELECT seq FROM balance_entries WHERE id = $1 AND account_id = $2`, after, accountID).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return HistoryPage{}, ErrInvalidCursor
		}
		if err != nil {
			return HistoryPage{}, err
		}
		before = &seq
	}
	rows, err := l.db.Query(ctx, `
		SELECT e.id, e.kind, e.available_delta, e.held_delta, e.available_after, e.held_after, e.ref_type, e.ref_id,
			COALESCE(h.job_id, ''), COALESCE(h.sku, ''), e.created_at
		FROM balance_entries e
		LEFT JOIN billing_holds h ON e.ref_type = 'hold' AND h.hold_id = e.ref_id
		WHERE e.account_id = $1 AND ($2::bigint IS NULL OR e.seq < $2)
		ORDER BY e.seq DESC
		LIMIT $3`, accountID, before, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (HistoryEntry, error) {
		var e HistoryEntry
		err := r.Scan(&e.ID, &e.Kind, &e.AvailableDelta, &e.HeldDelta, &e.AvailableAfter, &e.HeldAfter,
			&e.RefType, &e.RefID, &e.JobID, &e.SKU, &e.CreatedAt)
		return e, err
	})
	if err != nil {
		return HistoryPage{}, err
	}
	p := HistoryPage{Entries: entries}
	if len(entries) > limit {
		p.Entries, p.HasMore = entries[:limit], true
	}
	return p, nil
}

// SKUUsage is what one SKU (or the storage and egress meters, whose holds
// carry "storage" and "egress" as their SKU) cost the account in a period.
type SKUUsage struct {
	SKU        string
	GPUSeconds int64
	Quota      int64
}

// Usage totals the settles accepted in [from, to) by SKU: the money that
// actually left the account, so a month's total is what its settle entries
// sum to. A settle's gpu_seconds is cumulative for its hold, so each one
// counts the seconds it added over the previous settle of the same hold,
// computed over the hold's whole history before the period is cut.
func (l *Local) Usage(ctx context.Context, accountID int64, from, to time.Time) ([]SKUUsage, error) {
	rows, err := l.db.Query(ctx, `
		WITH s AS (
			SELECT h.sku, s.quota_delta, s.created_at,
				s.gpu_seconds - COALESCE(lag(s.gpu_seconds) OVER (PARTITION BY s.hold_id ORDER BY s.seq), 0) AS secs
			FROM billing_settlements s JOIN billing_holds h USING (hold_id)
			WHERE h.account_id = $1
		)
		SELECT sku, COALESCE(sum(secs), 0)::bigint, COALESCE(sum(quota_delta), 0)::bigint
		FROM s WHERE created_at >= $2 AND created_at < $3
		GROUP BY sku
		HAVING sum(quota_delta) > 0 OR sum(secs) > 0
		ORDER BY sku`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SKUUsage, error) {
		var u SKUUsage
		err := r.Scan(&u.SKU, &u.GPUSeconds, &u.Quota)
		return u, err
	})
}
