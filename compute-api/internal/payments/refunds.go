package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Refunds and disputes on top-ups (task B2; spec/accounts.md, "Top-up").
// Each is applied by the webhook in the transaction that stores its event,
// so a redelivered event is a duplicate before anything here runs. On top of
// that, every movement is keyed on what Stripe says happened rather than on
// the event: a refund on the charge's cumulative amount_refunded (so two
// events for one refund, or events out of order, debit the difference and
// never twice), a dispute on its own id.

const (
	// OutcomeRefunded: a refund was debited from the balance.
	OutcomeRefunded = "refunded"
	// OutcomeDisputed: a dispute was debited, the account suspended and its
	// live jobs cancelled.
	OutcomeDisputed = "disputed"
	// OutcomeDisputeWon and OutcomeDisputeLost close a dispute.
	OutcomeDisputeWon  = "dispute_won"
	OutcomeDisputeLost = "dispute_lost"
)

// Dispute statuses (topup_disputes.status).
const (
	disputeOpen = "open"
	disputeWon  = "won"
	disputeLost = "lost"
)

// Top-up statuses B2 adds.
const (
	StatusRefunded          = "refunded"
	StatusPartiallyRefunded = "partially_refunded"
	StatusDisputed          = "disputed"
	StatusLost              = "lost"
)

// Email kinds queued in payment_emails.
const (
	mailRefundIssued      = "refund_issued"
	mailAccountSuspended  = "account_suspended"
	mailOpsDisputeOpened  = "ops_dispute_opened"
	mailOpsDisputeClosed  = "ops_dispute_closed"
	mailOpsRefundNegative = "ops_refund_negative"
)

// JobCanceller is the normal cancel path (jobs.Service.Cancel): metering
// stops at acceptedAt and the hold settles what was used.
type JobCanceller interface {
	Cancel(ctx context.Context, owner, id string, acceptedAt time.Time) (store.Job, error)
}

// chargeEffects is what a committed charge event still has to do outside
// the transaction.
type chargeEffects struct {
	owner      string
	cancelAt   time.Time
	cancelJobs []string
}

type lockedTopup struct {
	id                         string
	accountID                  int64
	amount, refunded, disputed int64
	bonus, bonusReversed       int64
	status                     string
}

func lockTopup(ctx context.Context, tx pgx.Tx, id string) (lockedTopup, error) {
	t := lockedTopup{id: id}
	err := tx.QueryRow(ctx, `SELECT account_id, amount_cents, refunded_cents, disputed_cents, bonus_cents, bonus_reversed_cents, status
		FROM topups WHERE id = $1 FOR UPDATE`, id).
		Scan(&t.accountID, &t.amount, &t.refunded, &t.disputed, &t.bonus, &t.bonusReversed, &t.status)
	return t, err
}

// bonusShare is how much of a top-up's bonus goes with refunded plus
// disputed cents of it (ADR-0013 §3): the same proportion, rounded down,
// and all of it once nothing of the payment is left. Computed on running
// totals, so partial refunds never drift by rounding.
func bonusShare(amount, bonus, gone int64) int64 {
	if amount <= 0 || bonus == 0 {
		return 0
	}
	if gone >= amount {
		return bonus
	}
	return bonus * gone / amount
}

// takeBonus debits the bonus that refunded plus disputed cents now carry
// beyond what was already taken back, and answers how much it took. Like the
// refund or dispute it goes with, it may take available below zero.
func (s *Service) takeBonus(ctx context.Context, tx pgx.Tx, t lockedTopup, refunded, disputed int64, key string, detail map[string]any) (int64, error) {
	take := bonusShare(t.amount, t.bonus, refunded+disputed) - t.bonusReversed
	if take <= 0 {
		return 0, nil
	}
	if _, err := s.bill.ReverseTx(ctx, tx, billing.Reversal{AccountID: t.accountID, Kind: "topup_bonus", Quota: -take * quotaPerCent,
		RefType: "topup", RefID: t.id, Idempotency: key, Detail: detail}); err != nil {
		return 0, err
	}
	_, err := tx.Exec(ctx, `UPDATE topups SET bonus_reversed_cents = bonus_reversed_cents + $2 WHERE id = $1`, t.id, take)
	return take, err
}

// settledStatus is what a paid top-up is called once no dispute is open on
// it: by how much of it has been refunded.
func settledStatus(amount, refunded int64) string {
	switch {
	case refunded >= amount:
		return StatusRefunded
	case refunded > 0:
		return StatusPartiallyRefunded
	}
	return StatusCredited
}

func (s *Service) applyCharge(ctx context.Context, tx pgx.Tx, ev event, ref chargeRef, topupID string, log *slog.Logger) (string, *chargeEffects, error) {
	t, err := lockTopup(ctx, tx, topupID)
	if err != nil {
		return "", nil, err
	}
	log = log.With("topup", topupID, "account", t.accountID, "object", ref.ID)
	// The money has moved at Stripe whatever we think of the event, so an
	// event that cannot be applied as it is is not "rejected": it is kept,
	// and the operator is told to reconcile it by hand.
	unapplied := func(why string) (string, *chargeEffects, error) {
		log.Error("stripe webhook: refund or dispute on a top-up NOT applied; operator action needed", "why", why,
			"currency", ref.Currency, "amount", ref.Amount, "amount_refunded", ref.AmountRefunded, "amount_cents", t.amount, "status", ref.Status)
		return OutcomeRecorded, nil, nil
	}
	if ref.Currency != "usd" {
		return unapplied("currency is not usd")
	}
	switch ev.Type {
	case "charge.refunded":
		if ref.Amount != t.amount || ref.AmountRefunded < 0 || ref.AmountRefunded > t.amount {
			return unapplied("charge amount does not match the top-up")
		}
		out, err := s.applyRefund(ctx, tx, ev, ref, t, log)
		return out, nil, err
	case "charge.dispute.created":
		if ref.Amount <= 0 || ref.Amount > t.amount {
			return unapplied("disputed amount is not within the top-up")
		}
		return s.applyDisputeCreated(ctx, tx, ev, ref, t)
	default: // charge.dispute.closed
		if ref.Amount <= 0 || ref.Amount > t.amount {
			return unapplied("disputed amount is not within the top-up")
		}
		return s.applyDisputeClosed(ctx, tx, ev, ref, t, log)
	}
}

// applyRefund debits what the charge has had refunded since the last refund
// applied. A refund larger than available is debited in full: the money is
// back on the card, so the balance goes negative, which refuses every hold
// until it is paid back. Running jobs are left alone.
func (s *Service) applyRefund(ctx context.Context, tx pgx.Tx, ev event, ref chargeRef, t lockedTopup, log *slog.Logger) (string, error) {
	delta := ref.AmountRefunded - t.refunded
	if delta <= 0 {
		return OutcomeNoChange, nil
	}
	bal, err := s.bill.ReverseTx(ctx, tx, billing.Reversal{AccountID: t.accountID, Kind: "refund", Quota: -delta * quotaPerCent,
		RefType: "topup", RefID: t.id, Idempotency: fmt.Sprintf("refund:%s:%d", t.id, ref.AmountRefunded),
		Detail: map[string]any{"charge": ref.ID, "event": ev.ID, "refunded_total_cents": ref.AmountRefunded}})
	if err != nil {
		return "", err
	}
	bonus, err := s.takeBonus(ctx, tx, t, ref.AmountRefunded, t.disputed, fmt.Sprintf("topup_bonus_reversal:%s:refund:%d", t.id, ref.AmountRefunded),
		map[string]any{"event": ev.ID, "refunded_total_cents": ref.AmountRefunded})
	if err != nil {
		return "", err
	}
	if bonus > 0 {
		if bal, err = s.bill.Balance(ctx, t.accountID); err != nil {
			return "", err
		}
	}
	status := t.status
	if status != StatusDisputed && status != StatusLost {
		status = settledStatus(t.amount, ref.AmountRefunded)
	}
	if _, err := tx.Exec(ctx, `UPDATE topups SET refunded_cents = $2, status = $3 WHERE id = $1`, t.id, ref.AmountRefunded, status); err != nil {
		return "", err
	}
	now := s.now()
	// Any refund of the top-up answers its open request, whatever amount
	// was asked: the operator decided how much.
	if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status = 'refunded', refund_event = $2, resolved_at = $3
		WHERE topup_id = $1 AND status = 'open'`, t.id, ev.ID, now); err != nil {
		return "", err
	}
	if _, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorStripe, ActorID: ev.ID, AccountID: t.accountID,
		Action: accounts.ActionRefund, Detail: map[string]any{"stage": "applied", "topup_id": t.id, "charge": ref.ID,
			"amount_usd": CentsUSD(int(delta)), "bonus_reversed_usd": CentsUSD(int(bonus)), "refunded_total_usd": CentsUSD(int(ref.AmountRefunded)),
			"available_after_usd": billing.USD(bal.Available)}}); err != nil {
		return "", err
	}
	r := mailer.Refund{TopupID: t.id, AmountUSD: CentsUSD(int(delta)), TopupAmountUSD: CentsUSD(int(t.amount)), AvailableUSD: billing.USD(bal.Available), At: now}
	if bonus > 0 {
		r.BonusReversedUSD = CentsUSD(int(bonus))
	}
	if err := s.queueAccount(ctx, tx, ev.ID, mailRefundIssued, t.accountID, r); err != nil {
		return "", err
	}
	if bal.Available < 0 {
		log.Error("refund left the account's balance negative; operator action needed", "available_usd", billing.USD(bal.Available))
		if err := s.queueOperator(ctx, tx, ev.ID, mailOpsRefundNegative, t.accountID, mailer.OperatorNotice{
			Title: "refund left account " + strconv.FormatInt(t.accountID, 10) + " negative",
			Body: "A refund of USD " + CentsUSD(int(delta)) + " on " + t.id + " was larger than the account's unused balance, so its available balance is now negative. " +
				"The account cannot start jobs until it is paid back; running jobs were not stopped.",
			Action: "Decide whether to recover the amount or write it off (admin balance credit with a reason).",
			Facts: [][2]string{{"Account", strconv.FormatInt(t.accountID, 10)}, {"Top-up", t.id}, {"Refunded", "USD " + CentsUSD(int(delta))},
				{"Available", "USD " + billing.USD(bal.Available)}, {"Stripe event", ev.ID}},
		}); err != nil {
			return "", err
		}
	}
	return OutcomeRefunded, nil
}

// applyDisputeCreated debits the disputed amount, suspends the account and
// cancels its live jobs. A dispute already known (its closing event came
// first) changes nothing.
func (s *Service) applyDisputeCreated(ctx context.Context, tx pgx.Tx, ev event, d chargeRef, t lockedTopup) (string, *chargeEffects, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM topup_disputes WHERE id = $1 FOR UPDATE`, d.ID).Scan(&status)
	if err == nil {
		return OutcomeNoChange, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	eff, err := s.openDispute(ctx, tx, ev, d, t)
	if err != nil {
		return "", nil, err
	}
	return OutcomeDisputed, eff, nil
}

func (s *Service) openDispute(ctx context.Context, tx pgx.Tx, ev event, d chargeRef, t lockedTopup) (*chargeEffects, error) {
	now := s.now()
	bal, err := s.bill.ReverseTx(ctx, tx, billing.Reversal{AccountID: t.accountID, Kind: "dispute", Quota: -d.Amount * quotaPerCent,
		RefType: "dispute", RefID: d.ID, Idempotency: "dispute:" + d.ID,
		Detail: map[string]any{"topup_id": t.id, "event": ev.ID, "reason": d.Reason}})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE topups SET disputed_cents = disputed_cents + $2, status = $3 WHERE id = $1`,
		t.id, d.Amount, StatusDisputed); err != nil {
		return nil, err
	}
	bonus, err := s.takeBonus(ctx, tx, t, t.refunded, t.disputed+d.Amount, "topup_bonus_reversal:"+d.ID,
		map[string]any{"dispute": d.ID, "event": ev.ID})
	if err != nil {
		return nil, err
	}
	if bonus > 0 {
		if bal, err = s.bill.Balance(ctx, t.accountID); err != nil {
			return nil, err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE accounts SET status = $2, suspended_reason = 'dispute' WHERE id = $1 AND status = $3`,
		t.accountID, accounts.AccountSuspended, accounts.AccountActive)
	if err != nil {
		return nil, err
	}
	suspended := tag.RowsAffected() == 1
	if _, err := tx.Exec(ctx, `INSERT INTO topup_disputes (id, topup_id, account_id, amount_cents, bonus_cents, reason, suspended_account, opened_event_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, d.ID, t.id, t.accountID, d.Amount, bonus, d.Reason, suspended, ev.ID, now); err != nil {
		return nil, err
	}
	audit := func(action string, detail map[string]any) error {
		_, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorStripe, ActorID: ev.ID, AccountID: t.accountID,
			Action: action, Detail: detail})
		return err
	}
	if err := audit(accounts.ActionDisputeOpened, map[string]any{"topup_id": t.id, "dispute": d.ID, "amount_usd": CentsUSD(int(d.Amount)),
		"reason": d.Reason, "available_after_usd": billing.USD(bal.Available)}); err != nil {
		return nil, err
	}
	if suspended {
		if err := audit(accounts.ActionAccountSuspended, map[string]any{"from": accounts.AccountActive, "to": accounts.AccountSuspended,
			"reason": "dispute", "dispute": d.ID}); err != nil {
			return nil, err
		}
	}
	owner := accounts.OwnerFor(t.accountID)
	jobsCancelled, err := store.RequestOwnerCancelsTx(ctx, tx, owner, now)
	if err != nil {
		return nil, err
	}
	if suspended {
		if err := s.queueAccount(ctx, tx, ev.ID, mailAccountSuspended, t.accountID,
			mailer.Suspension{TopupID: t.id, AmountUSD: CentsUSD(int(d.Amount)), At: now}); err != nil {
			return nil, err
		}
	}
	state := "suspended by this dispute"
	if !suspended {
		state = "was already suspended or closed; left as it was"
	}
	if err := s.queueOperator(ctx, tx, ev.ID, mailOpsDisputeOpened, t.accountID, mailer.OperatorNotice{
		Title: "dispute opened on " + t.id,
		Body: "A card payment for a Unify Compute top-up was disputed. The disputed amount was taken off the balance, the account " + state +
			", and its live jobs were cancelled.",
		Action: "Answer the dispute in the Stripe dashboard before its due date, with the account's usage and audit trail as evidence.",
		Facts: [][2]string{{"Account", strconv.FormatInt(t.accountID, 10)}, {"Top-up", t.id}, {"Dispute", d.ID}, {"Reason", d.Reason},
			{"Amount", "USD " + CentsUSD(int(d.Amount))}, {"Available after", "USD " + billing.USD(bal.Available)},
			{"Jobs cancelled", strconv.Itoa(len(jobsCancelled))}, {"Stripe event", ev.ID}},
	}); err != nil {
		return nil, err
	}
	return &chargeEffects{owner: owner, cancelAt: now, cancelJobs: jobsCancelled}, nil
}

// applyDisputeClosed reinstates a won dispute's amount and lifts the
// suspension if this dispute caused it and nothing else holds it; a lost one
// stays debited and the account stays suspended for the operator. Stripe
// does not promise order, so a close for a dispute never opened here is
// opened first when lost (the money is gone) and only recorded when won.
func (s *Service) applyDisputeClosed(ctx context.Context, tx pgx.Tx, ev event, d chargeRef, t lockedTopup, log *slog.Logger) (string, *chargeEffects, error) {
	won := d.Status == "won" || d.Status == "warning_closed"
	if !won && d.Status != "lost" {
		log.Warn("stripe webhook: dispute closed with an unknown status; nothing changed", "dispute_status", d.Status)
		return OutcomeNoChange, nil, nil
	}
	var row struct {
		status        string
		amount, bonus int64
		suspended     bool
	}
	err := tx.QueryRow(ctx, `SELECT status, amount_cents, bonus_cents, suspended_account FROM topup_disputes WHERE id = $1 FOR UPDATE`, d.ID).
		Scan(&row.status, &row.amount, &row.bonus, &row.suspended)
	var eff *chargeEffects
	switch {
	case errors.Is(err, pgx.ErrNoRows) && won:
		now := s.now()
		_, err := tx.Exec(ctx, `INSERT INTO topup_disputes (id, topup_id, account_id, amount_cents, status, reason, opened_event_id, closed_event_id, created_at, closed_at)
			VALUES ($1, $2, $3, $4, 'won', $5, $6, $6, $7, $7)`, d.ID, t.id, t.accountID, d.Amount, d.Reason, ev.ID, now)
		return OutcomeNoChange, nil, err
	case errors.Is(err, pgx.ErrNoRows):
		if eff, err = s.openDispute(ctx, tx, ev, d, t); err != nil {
			return "", nil, err
		}
		row.status, row.amount = disputeOpen, d.Amount
		if err := tx.QueryRow(ctx, `SELECT bonus_cents FROM topup_disputes WHERE id = $1`, d.ID).Scan(&row.bonus); err != nil {
			return "", nil, err
		}
	case err != nil:
		return "", nil, err
	}
	if row.status != disputeOpen {
		return OutcomeNoChange, nil, nil
	}
	now := s.now()
	to := map[bool]string{true: disputeWon, false: disputeLost}[won]
	if _, err := tx.Exec(ctx, `UPDATE topup_disputes SET status = $2, closed_event_id = $3, closed_at = $4 WHERE id = $1`, d.ID, to, ev.ID, now); err != nil {
		return "", nil, err
	}
	var bal billing.Balance
	lifted := false
	why := ""
	if won {
		if bal, err = s.bill.ReverseTx(ctx, tx, billing.Reversal{AccountID: t.accountID, Kind: "dispute_reversal", Quota: row.amount * quotaPerCent,
			RefType: "dispute", RefID: d.ID, Idempotency: "dispute_reversal:" + d.ID,
			Detail: map[string]any{"topup_id": t.id, "event": ev.ID}}); err != nil {
			return "", nil, err
		}
		// The bonus this dispute took back comes back with the money.
		if row.bonus > 0 {
			if bal, err = s.bill.ReverseTx(ctx, tx, billing.Reversal{AccountID: t.accountID, Kind: "topup_bonus", Quota: row.bonus * quotaPerCent,
				RefType: "dispute", RefID: d.ID, Idempotency: "topup_bonus_restore:" + d.ID,
				Detail: map[string]any{"topup_id": t.id, "event": ev.ID}}); err != nil {
				return "", nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE topups SET bonus_reversed_cents = GREATEST(bonus_reversed_cents - $2, 0) WHERE id = $1`,
				t.id, row.bonus); err != nil {
				return "", nil, err
			}
		}
		var stillOpen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topup_disputes WHERE topup_id = $1 AND status = 'open')`, t.id).Scan(&stillOpen); err != nil {
			return "", nil, err
		}
		status := StatusDisputed
		if !stillOpen {
			status = settledStatus(t.amount, t.refunded)
		}
		if _, err := tx.Exec(ctx, `UPDATE topups SET disputed_cents = GREATEST(disputed_cents - $2, 0), status = $3 WHERE id = $1`,
			t.id, row.amount, status); err != nil {
			return "", nil, err
		}
		lifted, why, err = s.liftDisputeSuspension(ctx, tx, ev, t.accountID, row.suspended, bal)
		if err != nil {
			return "", nil, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE topups SET status = $2 WHERE id = $1`, t.id, StatusLost); err != nil {
			return "", nil, err
		}
		if bal, err = s.bill.Balance(ctx, t.accountID); err != nil {
			return "", nil, err
		}
		why = "the dispute was lost; the account stays suspended until an operator reinstates it"
	}
	if _, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorStripe, ActorID: ev.ID, AccountID: t.accountID,
		Action: accounts.ActionDisputeClosed, Detail: map[string]any{"topup_id": t.id, "dispute": d.ID, "outcome": to,
			"amount_usd": CentsUSD(int(row.amount)), "suspension_lifted": lifted}}); err != nil {
		return "", nil, err
	}
	state := "Suspension lifted."
	action := "Nothing to do."
	if !lifted {
		state = "Suspension not lifted: " + why + "."
		action = "Review the account; reinstate it with `compute-api admin account reinstate` when it is settled."
	}
	body := "The dispute was won and USD " + CentsUSD(int(row.amount)) + " went back on the balance. " + state
	if !won {
		body = "The dispute was lost: the amount stays debited. " + state
	}
	if err := s.queueOperator(ctx, tx, ev.ID, mailOpsDisputeClosed, t.accountID, mailer.OperatorNotice{
		Title: "dispute " + to + " on " + t.id, Body: body, Action: action,
		Facts: [][2]string{{"Account", strconv.FormatInt(t.accountID, 10)}, {"Top-up", t.id}, {"Dispute", d.ID},
			{"Amount", "USD " + CentsUSD(int(row.amount))}, {"Available", "USD " + billing.USD(bal.Available)}, {"Stripe event", ev.ID}},
	}); err != nil {
		return "", nil, err
	}
	if won {
		return OutcomeDisputeWon, eff, nil
	}
	return OutcomeDisputeLost, eff, nil
}

// liftDisputeSuspension reactivates the account after a won dispute only if
// that dispute is what suspended it, no other dispute on the account is
// open, and the balance is not negative (the operator's reinstate refuses a
// negative balance too).
func (s *Service) liftDisputeSuspension(ctx context.Context, tx pgx.Tx, ev event, accountID int64, causedByThis bool, bal billing.Balance) (bool, string, error) {
	if !causedByThis {
		return false, "this dispute did not suspend the account", nil
	}
	var status, reason string
	if err := tx.QueryRow(ctx, `SELECT status, coalesce(suspended_reason, '') FROM accounts WHERE id = $1 FOR UPDATE`, accountID).
		Scan(&status, &reason); err != nil {
		return false, "", err
	}
	if status != accounts.AccountSuspended || reason != "dispute" {
		return false, "the account is " + status + " for another reason", nil
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM topup_disputes WHERE account_id = $1 AND status = 'open'`, accountID).Scan(&open); err != nil {
		return false, "", err
	}
	if open > 0 {
		return false, strconv.Itoa(open) + " other dispute(s) on the account are still open", nil
	}
	if bal.Available < 0 {
		return false, "the balance is still negative (USD " + billing.USD(bal.Available) + ")", nil
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET status = $2, suspended_reason = NULL WHERE id = $1`, accountID, accounts.AccountActive); err != nil {
		return false, "", err
	}
	_, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorStripe, ActorID: ev.ID, AccountID: accountID,
		Action: accounts.ActionAccountReinstated, Detail: map[string]any{"from": accounts.AccountSuspended, "to": accounts.AccountActive,
			"reason": "dispute won"}})
	return err == nil, "", err
}

// cancelJobs carries out a committed dispute's cancels through the normal
// cancel path. The requests are already durable, so a failure here only
// delays them to the worker-lock holder's next reconcile.
func (s *Service) cancelJobs(ctx context.Context, eff *chargeEffects, log *slog.Logger) {
	if eff == nil || len(eff.cancelJobs) == 0 {
		return
	}
	if s.jobs == nil {
		log.Warn("dispute: job cancels recorded; no job service here, the worker carries them out", "jobs", len(eff.cancelJobs))
		return
	}
	for _, id := range eff.cancelJobs {
		if _, err := s.jobs.Cancel(context.WithoutCancel(ctx), eff.owner, id, eff.cancelAt); err != nil {
			log.Warn("dispute: cancel recorded, not yet carried out", "job", id, "err", err)
		}
	}
}
