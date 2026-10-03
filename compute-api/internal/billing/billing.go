// Package billing is compute's own balance and the local biller
// (docs/spec/accounts.md, "balance_entries" and "Local holds"; ADR-0011).
//
// Local places, settles, extends and releases holds against an account's
// balance in compute's Postgres, with the console bridge's contract
// (docs/spec/console-billing-bridge.md): the same idempotency on hold_id,
// event_id and extend_id, and the same refusals. A refusal is a
// *console.Error carrying the status and code the console would have sent,
// so the ledger classifies a local answer exactly as it classifies the
// console's and needs no logic of its own for either. That shared contract is
// the only thing taken from the bridge; nothing here talks to the console.
//
// Every movement of a balance writes the accounts row and one balance_entries
// row in the same transaction, under SELECT … FOR UPDATE on the account, so
// for every account the entries' deltas sum to available and held.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// The bridge's refusal codes (console-billing-bridge.md, "Errors and 404"),
// sent in console.Error.Message as the console sends them in `message`.
const (
	CodeInvalidRequest           = "invalid_request"
	CodeInsufficientBalance      = "insufficient_balance"
	CodeHoldNotFound             = console.CodeHoldNotFound
	CodeHoldExceeded             = "hold_exceeded"
	CodeHoldExpired              = "hold_expired"
	CodeHoldClosed               = "hold_closed"
	CodeIdempotencyConflict      = console.CodeIdempotencyConflict
	CodeSKUMismatch              = "sku_mismatch"
	CodeCumulativeQuotaRegressed = "cumulative_quota_regressed"
)

// MaxExtendIDLen bounds extend_id, as the bridge does.
const MaxExtendIDLen = 64

// Hold statuses. Only an open hold moves balance.
const (
	HoldOpen     = "open"
	HoldSettled  = "settled"
	HoldReleased = "released"
	HoldExpired  = "expired"
)

var (
	ErrUnknownAccount = errors.New("billing: no such account")
	// ErrComputeNotEnabled and ErrAccountSuspended refuse a submit
	// (accounts.md, "Job API changes"): 403 compute_not_enabled and 403
	// account_suspended.
	ErrComputeNotEnabled = errors.New("billing: compute is not enabled for this account")
	ErrAccountSuspended  = errors.New("billing: account is not active")
	ErrInsufficientFunds = errors.New("billing: debit larger than the available balance")
)

func refuse(status int, code string) error { return &console.Error{Status: status, Message: code} }

type Local struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool, now func() time.Time) *Local {
	if now == nil {
		now = time.Now
	}
	return &Local{db: db, now: now}
}

// unix is the clock at the bridge's resolution: expiry is decided in whole
// seconds, as expires_at is sent.
func (l *Local) unix() time.Time { return time.Unix(l.now().Unix(), 0) }

type account struct {
	id, available, held int64
}

func lockAccount(ctx context.Context, tx pgx.Tx, id int64) (*account, error) {
	a := &account{id: id}
	err := tx.QueryRow(ctx, `SELECT available, held FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&a.available, &a.held)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownAccount
	}
	return a, err
}

// entry is one balance movement, written by move.
type entry struct {
	kind                  string
	available, held       int64
	refType, refID, idemp string
	detail                any
}

func (l *Local) move(ctx context.Context, tx pgx.Tx, a *account, e entry) error {
	a.available += e.available
	a.held += e.held
	detail, err := json.Marshal(e.detail)
	if err != nil {
		return err
	}
	if e.detail == nil {
		detail = []byte(`{}`)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET available = $2, held = $3 WHERE id = $1`, a.id, a.available, a.held); err != nil {
		return fmt.Errorf("billing: update balance: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO balance_entries (id, account_id, kind, available_delta, held_delta, available_after, held_after,
			ref_type, ref_id, idempotency_key, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		store.NewID("be_", l.now()), a.id, e.kind, e.available, e.held, a.available, a.held, e.refType, e.refID, e.idemp, detail)
	if err != nil {
		return fmt.Errorf("billing: balance entry: %w", err)
	}
	return nil
}

type holdRow struct {
	id, sku                  string
	accountID                int64
	quota, settled, released int64
	status                   string
}

func loadHoldForUpdate(ctx context.Context, tx pgx.Tx, id string) (*holdRow, error) {
	h := &holdRow{id: id}
	err := tx.QueryRow(ctx, `
		SELECT account_id, sku, quota, settled_quota, released_quota, status FROM billing_holds WHERE hold_id = $1 FOR UPDATE`,
		id).Scan(&h.accountID, &h.sku, &h.quota, &h.settled, &h.released, &h.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// close ends an open hold, returning whatever was not settled to available.
func (l *Local) close(ctx context.Context, tx pgx.Tx, a *account, h *holdRow, status string) error {
	h.released, h.status = h.quota-h.settled, status
	if h.released > 0 {
		if err := l.move(ctx, tx, a, entry{kind: "release", available: h.released, held: -h.released,
			refType: "hold", refID: h.id, idemp: "release:" + h.id, detail: map[string]string{"closed_by": status}}); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE billing_holds SET status = $2, released_quota = $3, closed_at = now() WHERE hold_id = $1`,
		h.id, h.status, h.released)
	return err
}

// expire closes the account's open holds past their expires_at. Every verb
// runs it under the account lock before it looks at a hold or the balance,
// as the console's sweeper runs before each bridge request, so an expired
// hold is refused and its remainder is spendable the moment it expires,
// not only after the next hourly sweep.
func (l *Local) expire(ctx context.Context, tx pgx.Tx, a *account) error {
	rows, err := tx.Query(ctx, `
		SELECT hold_id FROM billing_holds WHERE account_id = $1 AND status = 'open' AND expires_at < $2
		ORDER BY created_at, hold_id`, a.id, l.unix())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		h, err := loadHoldForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := l.close(ctx, tx, a, h, HoldExpired); err != nil {
			return err
		}
	}
	return nil
}

// lockHold locks the hold's account, then the hold, after expiring the
// account's due holds. A nil hold with a nil error means no such hold.
func (l *Local) lockHold(ctx context.Context, tx pgx.Tx, holdID string) (*account, *holdRow, error) {
	var accountID int64
	err := tx.QueryRow(ctx, `SELECT account_id FROM billing_holds WHERE hold_id = $1`, holdID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if err := l.expire(ctx, tx, a); err != nil {
		return nil, nil, err
	}
	h, err := loadHoldForUpdate(ctx, tx, holdID)
	return a, h, err
}

// active is the bridge's refusal for a mutating call on a hold that is not
// open, or nil.
func active(h *holdRow) error {
	switch {
	case h == nil:
		return refuse(http.StatusNotFound, CodeHoldNotFound)
	case h.status == HoldExpired:
		return refuse(http.StatusConflict, CodeHoldExpired)
	case h.status != HoldOpen:
		return refuse(http.StatusConflict, CodeHoldClosed)
	}
	return nil
}

// tx runs fn in a transaction. A refusal (a *console.Error) still commits:
// a verb refuses before writing anything of its own, but the holds it
// expired on the way stay expired and their remainder stays returned, as
// with the console's sweep. Rolling that back would leave the balance short
// until the next call or sweep.
func (l *Local) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	var refusal error
	err := pgx.BeginFunc(ctx, l.db, func(tx pgx.Tx) error {
		err := fn(tx)
		var ce *console.Error
		if errors.As(err, &ce) {
			refusal = err
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return refusal
}

// --- the bridge verbs ----------------------------------------------------

// Hold reserves req.Quota of the account req.UserID names. A replay of the
// same request answers what the first answered; a different request under
// the same hold_id is idempotency_conflict. Unlike the console there is no
// per-key spending limit (accounts.md, open point 3), so TokenID is
// recorded and not checked.
func (l *Local) Hold(ctx context.Context, req console.HoldRequest) (*console.HoldResult, error) {
	if req.HoldID == "" || req.Quota <= 0 {
		return nil, refuse(http.StatusBadRequest, CodeInvalidRequest)
	}
	var out console.HoldResult
	err := l.tx(ctx, func(tx pgx.Tx) error {
		a, err := lockAccount(ctx, tx, int64(req.UserID))
		if errors.Is(err, ErrUnknownAccount) {
			if replayed, rerr := holdReplay(ctx, tx, req, &out); replayed || rerr != nil {
				return rerr
			}
			return refuse(http.StatusBadRequest, CodeInvalidRequest)
		}
		if err != nil {
			return err
		}
		if err := l.expire(ctx, tx, a); err != nil {
			return err
		}
		// After the lock, so a concurrent create of the same hold_id has
		// committed or not started.
		if replayed, err := holdReplay(ctx, tx, req, &out); replayed || err != nil {
			return err
		}
		if a.available < req.Quota {
			return refuse(http.StatusPaymentRequired, CodeInsufficientBalance)
		}
		if err := l.move(ctx, tx, a, entry{kind: "hold", available: -req.Quota, held: req.Quota,
			refType: "hold", refID: req.HoldID, idemp: "hold:" + req.HoldID,
			detail: map[string]any{"job_id": req.JobID, "sku": req.SKU}}); err != nil {
			return err
		}
		out = console.HoldResult{HoldID: req.HoldID, Quota: req.Quota, RemainQuota: a.available}
		reqJSON, _ := json.Marshal(req)
		respJSON, _ := json.Marshal(out)
		var expires *time.Time
		if req.ExpiresAt > 0 {
			t := time.Unix(req.ExpiresAt, 0)
			expires = &t
		}
		var jobID *string
		if req.JobID != "" {
			jobID = &req.JobID
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO billing_holds (hold_id, account_id, key_id, job_id, sku, quota, expires_at, request, response)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			req.HoldID, a.id, req.TokenID, jobID, req.SKU, req.Quota, expires, reqJSON, respJSON)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// holdReplay answers a hold_id seen before: compared field by field with the
// original request, as the console does.
func holdReplay(ctx context.Context, tx pgx.Tx, req console.HoldRequest, out *console.HoldResult) (bool, error) {
	var reqJSON, respJSON []byte
	err := tx.QueryRow(ctx, `SELECT request, response FROM billing_holds WHERE hold_id = $1`, req.HoldID).Scan(&reqJSON, &respJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var prev console.HoldRequest
	if err := json.Unmarshal(reqJSON, &prev); err != nil {
		return false, err
	}
	if prev != req {
		return true, refuse(http.StatusConflict, CodeIdempotencyConflict)
	}
	return true, json.Unmarshal(respJSON, out)
}

// Settle charges up to req.CumulativeQuota of the hold. Amounts are
// cumulative, so a retried or superseded event charges nothing twice; a
// final settle closes the hold and returns the remainder.
func (l *Local) Settle(ctx context.Context, holdID string, req console.SettleRequest) (*console.SettleResult, error) {
	if req.EventID == "" || req.CumulativeQuota < 0 {
		return nil, refuse(http.StatusBadRequest, CodeInvalidRequest)
	}
	var out console.SettleResult
	err := l.tx(ctx, func(tx pgx.Tx) error {
		a, h, err := l.lockHold(ctx, tx, holdID)
		if err != nil {
			return err
		}
		// After the lock, so a concurrent settle of the same event has
		// committed or not started.
		if replayed, err := l.settleReplay(ctx, tx, h, holdID, req.EventID, &out); replayed || err != nil {
			return err
		}
		if err := active(h); err != nil {
			return err
		}
		// After the hold lookup, so a settle on an unknown hold still says
		// hold_not_found, matching the console's order.
		switch {
		case req.SKU == "":
			return refuse(http.StatusBadRequest, CodeInvalidRequest)
		case req.SKU != h.sku:
			return refuse(http.StatusConflict, CodeSKUMismatch)
		case req.CumulativeQuota > h.quota:
			return refuse(http.StatusConflict, CodeHoldExceeded)
		case req.CumulativeQuota < h.settled:
			return refuse(http.StatusConflict, CodeCumulativeQuotaRegressed)
		}
		delta := req.CumulativeQuota - h.settled
		h.settled = req.CumulativeQuota
		if delta > 0 {
			if err := l.move(ctx, tx, a, entry{kind: "settle", held: -delta, refType: "hold", refID: h.id,
				idemp: "settle:" + req.EventID, detail: map[string]any{"sku": h.sku, "gpu_seconds": req.GPUSeconds}}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE billing_holds SET settled_quota = $2 WHERE hold_id = $1`, h.id, h.settled); err != nil {
				return err
			}
		}
		if req.Final {
			if err := l.close(ctx, tx, a, h, HoldSettled); err != nil {
				return err
			}
		}
		out = console.SettleResult{HoldID: h.id, SettledQuota: h.settled, HoldRemaining: h.quota - h.settled - h.released}
		respJSON, _ := json.Marshal(out)
		_, err = tx.Exec(ctx, `
			INSERT INTO billing_settlements (event_id, hold_id, quota_delta, gpu_seconds, final, response)
			VALUES ($1, $2, $3, $4, $5, $6)`, req.EventID, h.id, delta, req.GPUSeconds, req.Final, respJSON)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// settleReplay answers an event_id seen before. The id is unique across
// every hold: reusing it on another hold is a conflict.
func (l *Local) settleReplay(ctx context.Context, tx pgx.Tx, h *holdRow, holdID, eventID string, out *console.SettleResult) (bool, error) {
	var prevHold string
	var respJSON []byte
	err := tx.QueryRow(ctx, `SELECT hold_id, response FROM billing_settlements WHERE event_id = $1`, eventID).Scan(&prevHold, &respJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if prevHold != holdID {
		return true, l.conflictOn(ctx, tx, h, prevHold)
	}
	return true, json.Unmarshal(respJSON, out)
}

// conflictOn is the idempotency_conflict for an id first used on prevHold.
// When the hold in the request does not exist, no account was locked and
// expired on the way here, so prevHold's is: the console sweeps every hold
// before each request, and the balances must not differ from its whatever
// the answer. (With both holds known, only the requested hold's account is
// expired; prevHold's waits for its next call or the hourly sweep. Locking a
// second account here could deadlock with a call that locks them the other
// way round, and the answer is the same.)
func (l *Local) conflictOn(ctx context.Context, tx pgx.Tx, h *holdRow, prevHold string) error {
	if h == nil {
		if _, _, err := l.lockHold(ctx, tx, prevHold); err != nil {
			return err
		}
	}
	return refuse(http.StatusConflict, CodeIdempotencyConflict)
}

// Extend reserves req.AdditionalQuota more on an open hold. A replayed
// extend_id answers with the hold's current state, as the console does,
// even after the hold has closed.
func (l *Local) Extend(ctx context.Context, holdID string, req console.ExtendRequest) (*console.HoldResult, error) {
	if req.AdditionalQuota <= 0 || req.ExtendID == "" || len(req.ExtendID) > MaxExtendIDLen {
		return nil, refuse(http.StatusBadRequest, CodeInvalidRequest)
	}
	var out console.HoldResult
	err := l.tx(ctx, func(tx pgx.Tx) error {
		replay := func(a *account, h *holdRow) (bool, error) {
			var prevHold string
			var prevAmount int64
			err := tx.QueryRow(ctx, `SELECT hold_id, additional_quota FROM billing_extensions WHERE extend_id = $1`, req.ExtendID).
				Scan(&prevHold, &prevAmount)
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if prevHold != holdID || prevAmount != req.AdditionalQuota {
				return true, l.conflictOn(ctx, tx, h, prevHold)
			}
			out = console.HoldResult{HoldID: h.id, Quota: h.quota, RemainQuota: a.available}
			return true, nil
		}
		a, h, err := l.lockHold(ctx, tx, holdID)
		if err != nil {
			return err
		}
		// Before the hold's state, so a retry of an extend that committed
		// still succeeds after the hold has closed; and an extend_id
		// recorded against another hold is a conflict even when this one
		// does not exist.
		if replayed, err := replay(a, h); replayed || err != nil {
			return err
		}
		if err := active(h); err != nil {
			return err
		}
		if a.available < req.AdditionalQuota {
			return refuse(http.StatusPaymentRequired, CodeInsufficientBalance)
		}
		if err := l.move(ctx, tx, a, entry{kind: "extend", available: -req.AdditionalQuota, held: req.AdditionalQuota,
			refType: "hold", refID: h.id, idemp: "extend:" + req.ExtendID}); err != nil {
			return err
		}
		h.quota += req.AdditionalQuota
		if _, err := tx.Exec(ctx, `UPDATE billing_holds SET quota = $2 WHERE hold_id = $1`, h.id, h.quota); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO billing_extensions (extend_id, hold_id, additional_quota) VALUES ($1, $2, $3)`,
			req.ExtendID, h.id, req.AdditionalQuota); err != nil {
			return err
		}
		out = console.HoldResult{HoldID: h.id, Quota: h.quota, RemainQuota: a.available}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Release closes an open hold and returns what was not settled. Releasing a
// hold already closed by release or by a final settle answers what was
// released then and moves nothing; an expired hold is hold_expired.
func (l *Local) Release(ctx context.Context, holdID string) (*console.ReleaseResult, error) {
	var out console.ReleaseResult
	err := l.tx(ctx, func(tx pgx.Tx) error {
		a, h, err := l.lockHold(ctx, tx, holdID)
		if err != nil {
			return err
		}
		if h != nil && (h.status == HoldReleased || h.status == HoldSettled) {
			out = console.ReleaseResult{ReleasedQuota: h.released, RemainQuota: a.available}
			return nil
		}
		if err := active(h); err != nil {
			return err
		}
		if err := l.close(ctx, tx, a, h, HoldReleased); err != nil {
			return err
		}
		out = console.ReleaseResult{ReleasedQuota: h.released, RemainQuota: a.available}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Sweep expires every open hold past its expires_at, account by account,
// and reports how many it closed. The verbs expire an account's holds as
// they touch it; this hourly pass covers accounts nothing touches.
func (l *Local) Sweep(ctx context.Context) (int, error) {
	rows, err := l.db.Query(ctx, `SELECT DISTINCT account_id FROM billing_holds WHERE status = 'open' AND expires_at < $1`, l.unix())
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	total := 0
	for _, id := range ids {
		err := l.tx(ctx, func(tx pgx.Tx) error {
			a, err := lockAccount(ctx, tx, id)
			if err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM billing_holds WHERE account_id = $1 AND status = 'open' AND expires_at < $2`,
				id, l.unix()).Scan(&n); err != nil {
				return err
			}
			total += n
			return l.expire(ctx, tx, a)
		})
		if err != nil {
			return total, fmt.Errorf("billing: sweep account %d: %w", id, err)
		}
	}
	return total, nil
}

// --- the submit gate -------------------------------------------------------

// CheckSubmit refuses a submit from an account that is not active or does
// not have compute enabled (ADR-0011 D3: off until the operator turns it on).
func (l *Local) CheckSubmit(ctx context.Context, accountID int) error {
	var status string
	var enabled bool
	err := l.db.QueryRow(ctx, `SELECT status, compute_enabled FROM accounts WHERE id = $1`, accountID).Scan(&status, &enabled)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrUnknownAccount
	case err != nil:
		return err
	case status != "active":
		return ErrAccountSuspended
	case !enabled:
		return ErrComputeNotEnabled
	}
	return nil
}

// --- balance ---------------------------------------------------------------

type Balance struct {
	Available, Held int64
}

func (l *Local) Balance(ctx context.Context, accountID int64) (Balance, error) {
	var b Balance
	err := l.db.QueryRow(ctx, `SELECT available, held FROM accounts WHERE id = $1`, accountID).Scan(&b.Available, &b.Held)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrUnknownAccount
	}
	return b, err
}

// Adjustment is a manual movement of available balance: an operator's credit
// or debit (admin_credit, admin_debit). RefID names what caused it (the
// audit event's id) and keys its idempotency.
type Adjustment struct {
	AccountID int64
	Quota     int64 // positive: credit; negative: debit
	RefType   string
	RefID     string
	Detail    any
}

// AdjustTx applies a within the caller's transaction, so the caller can write
// its audit event in the same commit. A debit never takes available below
// zero: money already held or spent cannot be taken back this way.
func (l *Local) AdjustTx(ctx context.Context, tx pgx.Tx, adj Adjustment) (Balance, error) {
	if adj.Quota == 0 {
		return Balance{}, errors.New("billing: adjustment of zero")
	}
	a, err := lockAccount(ctx, tx, adj.AccountID)
	if err != nil {
		return Balance{}, err
	}
	kind := "admin_credit"
	if adj.Quota < 0 {
		kind = "admin_debit"
		if a.available+adj.Quota < 0 {
			return Balance{}, ErrInsufficientFunds
		}
	}
	if err := l.move(ctx, tx, a, entry{kind: kind, available: adj.Quota, refType: adj.RefType, refID: adj.RefID,
		idemp: adj.RefType + ":" + adj.RefID, detail: adj.Detail}); err != nil {
		return Balance{}, err
	}
	return Balance{Available: a.available, Held: a.held}, nil
}

// CreditTopupTx adds a paid top-up to available within the caller's
// transaction, so the Stripe webhook commits the top-up's state, its
// stripe_events row, the entry and the audit row together. The entry's
// idempotency key is topup:<id>, so the same top-up can never be credited
// twice even if the caller's own check were bypassed. Neither the account's
// status nor its balance cap refuses it: the money has already been taken.
func (l *Local) CreditTopupTx(ctx context.Context, tx pgx.Tx, accountID int64, quota int64, topupID string) (Balance, error) {
	if quota <= 0 {
		return Balance{}, errors.New("billing: top-up of zero or less")
	}
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return Balance{}, err
	}
	if err := l.move(ctx, tx, a, entry{kind: "topup", available: quota, refType: "topup", refID: topupID,
		idemp: "topup:" + topupID}); err != nil {
		return Balance{}, err
	}
	return Balance{Available: a.available, Held: a.held}, nil
}

// Mismatch is an account whose balance columns differ from the sum of its
// entries' deltas.
type Mismatch struct {
	AccountID             int64
	Available, Held       int64
	SumAvailable, SumHeld int64
}

// Audit checks the balance invariant for one account, or for all when
// accountID is nil, and returns the accounts that break it.
func (l *Local) Audit(ctx context.Context, accountID *int64) ([]Mismatch, error) {
	rows, err := l.db.Query(ctx, `
		SELECT a.id, a.available, a.held, COALESCE(sum(e.available_delta), 0)::bigint, COALESCE(sum(e.held_delta), 0)::bigint
		FROM accounts a LEFT JOIN balance_entries e ON e.account_id = a.id
		WHERE $1::bigint IS NULL OR a.id = $1
		GROUP BY a.id, a.available, a.held
		HAVING a.available <> COALESCE(sum(e.available_delta), 0) OR a.held <> COALESCE(sum(e.held_delta), 0)
		ORDER BY a.id`, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Mismatch, error) {
		var m Mismatch
		err := r.Scan(&m.AccountID, &m.Available, &m.Held, &m.SumAvailable, &m.SumHeld)
		return m, err
	})
}

// --- reads for tests and the dashboard --------------------------------------

type HoldRecord struct {
	HoldID    string
	AccountID int64
	KeyID     int64
	JobID     string
	SKU       string
	Quota     int64
	Settled   int64
	Released  int64
	Status    string
	ExpiresAt int64 // Unix seconds; 0 never expires
}

func (l *Local) HoldRecord(ctx context.Context, holdID string) (HoldRecord, bool, error) {
	h := HoldRecord{HoldID: holdID}
	var jobID *string
	var expires *time.Time
	err := l.db.QueryRow(ctx, `
		SELECT account_id, key_id, job_id, sku, quota, settled_quota, released_quota, status, expires_at
		FROM billing_holds WHERE hold_id = $1`, holdID).
		Scan(&h.AccountID, &h.KeyID, &jobID, &h.SKU, &h.Quota, &h.Settled, &h.Released, &h.Status, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return HoldRecord{}, false, nil
	}
	if err != nil {
		return HoldRecord{}, false, err
	}
	if jobID != nil {
		h.JobID = *jobID
	}
	if expires != nil {
		h.ExpiresAt = expires.Unix()
	}
	return h, true, nil
}

type Settlement struct {
	EventID    string
	HoldID     string
	AccountID  int64
	KeyID      int64
	JobID      string
	SKU        string
	Quota      int64
	GPUSeconds int64
	Final      bool
}

// Settlements lists every accepted settle in write order.
func (l *Local) Settlements(ctx context.Context) ([]Settlement, error) {
	rows, err := l.db.Query(ctx, `
		SELECT s.event_id, s.hold_id, h.account_id, h.key_id, COALESCE(h.job_id, ''), h.sku, s.quota_delta, s.gpu_seconds, s.final
		FROM billing_settlements s JOIN billing_holds h USING (hold_id) ORDER BY s.seq`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Settlement, error) {
		var s Settlement
		err := r.Scan(&s.EventID, &s.HoldID, &s.AccountID, &s.KeyID, &s.JobID, &s.SKU, &s.Quota, &s.GPUSeconds, &s.Final)
		return s, err
	})
}

type Entry struct {
	ID                             string
	AccountID                      int64
	Kind                           string
	AvailableDelta, HeldDelta      int64
	AvailableAfter, HeldAfter      int64
	RefType, RefID, IdempotencyKey string
	CreatedAt                      time.Time
}

// Entries lists an account's balance entries in write order.
func (l *Local) Entries(ctx context.Context, accountID int64) ([]Entry, error) {
	rows, err := l.db.Query(ctx, `
		SELECT id, account_id, kind, available_delta, held_delta, available_after, held_after, ref_type, ref_id, idempotency_key, created_at
		FROM balance_entries WHERE account_id = $1 ORDER BY seq`, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.ID, &e.AccountID, &e.Kind, &e.AvailableDelta, &e.HeldDelta, &e.AvailableAfter, &e.HeldAfter,
			&e.RefType, &e.RefID, &e.IdempotencyKey, &e.CreatedAt)
		return e, err
	})
}

// USD renders quota units as USD with 4 decimals, as balances cross every
// API (accounts.md, "Units").
func USD(q int64) string {
	return new(big.Rat).SetFrac64(q, sku.QuotaPerUSD).FloatString(4)
}
