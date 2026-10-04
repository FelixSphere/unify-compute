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

// Kinds is every balance_entries.kind, in the order the CHECK constraint
// lists them. A filter naming anything else is refused before the query.
var Kinds = []string{"topup", "hold", "extend", "settle", "release", "refund", "dispute", "dispute_reversal", "admin_credit", "admin_debit", "writeoff"}

func ValidKind(k string) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// HistoryFilter narrows HistoryFiltered. Empty Kinds means every kind.
type HistoryFilter struct {
	After string
	Limit int
	Kinds []string
}

// History lists an account's entries newest first, strictly older than the
// entry named by after (empty for the newest).
func (l *Local) History(ctx context.Context, accountID int64, after string, limit int) (HistoryPage, error) {
	return l.HistoryFiltered(ctx, accountID, HistoryFilter{After: after, Limit: limit})
}

// HistoryFiltered is History restricted to the given kinds: the console's
// log-type filter, on the ledger.
func (l *Local) HistoryFiltered(ctx context.Context, accountID int64, f HistoryFilter) (HistoryPage, error) {
	after, limit := f.After, f.Limit
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
	var kinds []string
	if len(f.Kinds) > 0 {
		kinds = f.Kinds
	}
	rows, err := l.db.Query(ctx, `
		SELECT e.id, e.kind, e.available_delta, e.held_delta, e.available_after, e.held_after, e.ref_type, e.ref_id,
			COALESCE(h.job_id, ''), COALESCE(h.sku, ''), e.created_at
		FROM balance_entries e
		LEFT JOIN billing_holds h ON e.ref_type = 'hold' AND h.hold_id = e.ref_id
		WHERE e.account_id = $1 AND ($2::bigint IS NULL OR e.seq < $2) AND ($4::text[] IS NULL OR e.kind = ANY($4))
		ORDER BY e.seq DESC
		LIMIT $3`, accountID, before, limit+1, kinds)
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

// settledGPU is the period-agnostic view every usage read starts from: one
// row per settle with the seconds it added over the previous settle of the
// same hold, plus the hold's SKU, job and key. A settle's gpu_seconds is
// cumulative for its hold, so the delta is taken over the hold's whole
// history before any period filter is applied.
const settledGPU = `
	SELECT h.sku, h.job_id, h.key_id, s.quota_delta, s.created_at,
		s.gpu_seconds - COALESCE(lag(s.gpu_seconds) OVER (PARTITION BY s.hold_id ORDER BY s.seq), 0) AS secs
	FROM billing_settlements s JOIN billing_holds h USING (hold_id)
	WHERE h.account_id = $1`

// DayUsage is one UTC day of GPU settles.
type DayUsage struct {
	Day        time.Time
	GPUSeconds int64
	Quota      int64
}

// UsageByDay groups the period's GPU settles by the UTC day they were
// accepted. Days with nothing settled are absent; the caller fills them.
// Storage and egress charges have no job and are not GPU time, so they are
// left out here and in UsageByKey.
func (l *Local) UsageByDay(ctx context.Context, accountID int64, from, to time.Time) ([]DayUsage, error) {
	rows, err := l.db.Query(ctx, `
		WITH s AS (`+settledGPU+`)
		SELECT date_trunc('day', created_at AT TIME ZONE 'UTC') AS day,
			COALESCE(sum(secs), 0)::bigint, COALESCE(sum(quota_delta), 0)::bigint
		FROM s WHERE job_id IS NOT NULL AND created_at >= $2 AND created_at < $3
		GROUP BY day ORDER BY day`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayUsage, error) {
		var u DayUsage
		err := r.Scan(&u.Day, &u.GPUSeconds, &u.Quota)
		u.Day = time.Date(u.Day.Year(), u.Day.Month(), u.Day.Day(), 0, 0, 0, 0, time.UTC)
		return u, err
	})
}

// KeyUsage is one API key's GPU settles in a period. Name and Display are
// empty when the key row no longer exists.
type KeyUsage struct {
	KeyID      int64
	Name       string
	Display    string
	Jobs       int64
	GPUSeconds int64
	Quota      int64
}

func (l *Local) UsageByKey(ctx context.Context, accountID int64, from, to time.Time) ([]KeyUsage, error) {
	rows, err := l.db.Query(ctx, `
		WITH s AS (`+settledGPU+`)
		SELECT s.key_id, COALESCE(k.name, ''), COALESCE(k.display, ''),
			count(DISTINCT s.job_id), COALESCE(sum(s.secs), 0)::bigint, COALESCE(sum(s.quota_delta), 0)::bigint
		FROM s LEFT JOIN api_keys k ON k.id = s.key_id
		WHERE s.job_id IS NOT NULL AND s.created_at >= $2 AND s.created_at < $3
		GROUP BY s.key_id, k.name, k.display
		ORDER BY sum(s.quota_delta) DESC, s.key_id`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (KeyUsage, error) {
		var u KeyUsage
		err := r.Scan(&u.KeyID, &u.Name, &u.Display, &u.Jobs, &u.GPUSeconds, &u.Quota)
		return u, err
	})
}

// JobUsage is one job's GPU settles in a period, with what the job and key
// tables still say about it. SettledAt is the period's last settle for the
// job and is the list order.
type JobUsage struct {
	JobID      string
	SKU        string
	Status     string
	KeyID      int64
	KeyName    string
	KeyDisplay string
	StartedAt  *time.Time
	FinishedAt *time.Time
	SettledAt  time.Time
	GPUSeconds int64
	Quota      int64
}

type JobUsagePage struct {
	Rows    []JobUsage
	HasMore bool
}

// JobUsageFilter narrows UsageJobs. Zero values mean no filter; After is the
// job id of the last row already shown.
type JobUsageFilter struct {
	SKU   string
	KeyID int64
	After string
	Limit int
}

// UsageJobs lists the jobs charged in a period, newest settle first, keyset
// paged on (settled_at, job_id). A cursor naming a job that was never held
// by this account is ErrInvalidCursor.
func (l *Local) UsageJobs(ctx context.Context, accountID int64, from, to time.Time, f JobUsageFilter) (JobUsagePage, error) {
	if f.After != "" {
		var ok bool
		err := l.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing_holds WHERE account_id = $1 AND job_id = $2)`,
			accountID, f.After).Scan(&ok)
		if err != nil {
			return JobUsagePage{}, err
		}
		if !ok {
			return JobUsagePage{}, ErrInvalidCursor
		}
	}
	rows, err := l.db.Query(ctx, `
		WITH s AS (`+settledGPU+`),
		j AS (
			SELECT job_id, max(created_at) AS settled_at, sum(secs)::bigint AS secs, sum(quota_delta)::bigint AS quota,
				min(key_id) AS key_id, min(sku) AS sku
			FROM s WHERE job_id IS NOT NULL AND created_at >= $2 AND created_at < $3
			GROUP BY job_id
		)
		SELECT j.job_id, j.sku, COALESCE(jb.status, ''), j.key_id, COALESCE(k.name, ''), COALESCE(k.display, ''),
			jb.started_at, jb.finished_at, j.settled_at, j.secs, j.quota
		FROM j LEFT JOIN jobs jb ON jb.id = j.job_id LEFT JOIN api_keys k ON k.id = j.key_id
		WHERE ($4 = '' OR j.sku = $4) AND ($5 = 0 OR j.key_id = $5)
			AND ($6 = '' OR (j.settled_at, j.job_id) < (SELECT settled_at, job_id FROM j WHERE job_id = $6))
		ORDER BY j.settled_at DESC, j.job_id DESC
		LIMIT $7`, accountID, from, to, f.SKU, f.KeyID, f.After, f.Limit+1)
	if err != nil {
		return JobUsagePage{}, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (JobUsage, error) {
		var u JobUsage
		err := r.Scan(&u.JobID, &u.SKU, &u.Status, &u.KeyID, &u.KeyName, &u.KeyDisplay,
			&u.StartedAt, &u.FinishedAt, &u.SettledAt, &u.GPUSeconds, &u.Quota)
		return u, err
	})
	if err != nil {
		return JobUsagePage{}, err
	}
	page := JobUsagePage{Rows: all}
	if len(all) > f.Limit {
		page.Rows, page.HasMore = all[:f.Limit], true
	}
	return page, nil
}
