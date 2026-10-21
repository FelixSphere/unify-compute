package payments

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
)

// RefundRequest is `compute-api admin topup refund`: an operator refunding a
// customer's unused balance to the card that paid for it.
type RefundRequest struct {
	TopupID string
	// Cents is the amount; zero refunds everything still refundable on the
	// top-up.
	Cents  int
	Actor  string
	Reason string
}

// RefundIssued is what Stripe answered.
type RefundIssued struct {
	RefundID  string
	Status    string
	AccountID int64
	Cents     int
}

// ErrRefund is a refusal; nothing was sent to Stripe.
type ErrRefund struct{ Msg string }

func (e *ErrRefund) Error() string { return e.Msg }

// RequestRefund creates the refund at Stripe and nothing else. The balance
// is debited when charge.refunded arrives, by the same code that applies a
// refund made in the Stripe dashboard, so there is one path that moves
// money. The refusals are the policy decided on 2026-10-14: unused prepaid
// balance can be refunded; compute already used cannot, so a refund larger
// than the account's available balance is refused.
func (s *Service) RequestRefund(ctx context.Context, r RefundRequest) (RefundIssued, error) {
	if strings.TrimSpace(r.Reason) == "" {
		return RefundIssued{}, &ErrRefund{"a refund needs a reason"}
	}
	if s.stripe == nil {
		return RefundIssued{}, &ErrRefund{"Stripe is not configured here (STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET)"}
	}
	var t struct {
		accountID                  int64
		amount, refunded, disputed int
		bonus, bonusReversed       int
		status                     string
		pi                         *string
	}
	err := s.db.QueryRow(ctx, `SELECT account_id, amount_cents, refunded_cents, disputed_cents, bonus_cents, bonus_reversed_cents, status,
		stripe_payment_intent_id FROM topups WHERE id = $1`,
		r.TopupID).Scan(&t.accountID, &t.amount, &t.refunded, &t.disputed, &t.bonus, &t.bonusReversed, &t.status, &t.pi)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefundIssued{}, &ErrRefund{"no top-up " + r.TopupID}
	}
	if err != nil {
		return RefundIssued{}, err
	}
	if t.status != StatusCredited && t.status != StatusPartiallyRefunded {
		return RefundIssued{}, &ErrRefund{fmt.Sprintf("top-up %s is %s; only a credited or partially refunded top-up can be refunded", r.TopupID, t.status)}
	}
	if t.pi == nil || *t.pi == "" {
		return RefundIssued{}, &ErrRefund{"top-up " + r.TopupID + " has no PaymentIntent recorded"}
	}
	left := t.amount - t.refunded - t.disputed
	cents := r.Cents
	if cents == 0 {
		cents = left
	}
	if cents <= 0 || cents > left {
		return RefundIssued{}, &ErrRefund{fmt.Sprintf("top-up %s has USD %s left to refund; asked for USD %s", r.TopupID, CentsUSD(left), CentsUSD(cents))}
	}
	bal, err := s.bill.Balance(ctx, t.accountID)
	if err != nil {
		return RefundIssued{}, err
	}
	// The refund takes its share of the top-up's bonus off the balance with
	// it (ADR-0013 §3), so both must fit in what is unused.
	debit := func(c int) int64 {
		bonus := bonusShare(int64(t.amount), int64(t.bonus), int64(t.refunded+t.disputed+c)) - int64(t.bonusReversed)
		return (int64(c) + max(bonus, 0)) * quotaPerCent
	}
	if debit(cents) > bal.Available {
		fits := 0
		for lo, hi := 0, cents; lo <= hi; {
			mid := (lo + hi) / 2
			if debit(mid) <= bal.Available {
				fits, lo = mid, mid+1
			} else {
				hi = mid - 1
			}
		}
		return RefundIssued{}, &ErrRefund{fmt.Sprintf("account %d has USD %s available; refunding USD %s (with its share of the bonus credit) would return compute already used. Refund at most USD %s",
			t.accountID, billing.USD(bal.Available), CentsUSD(cents), CentsUSD(fits))}
	}
	form := url.Values{"payment_intent": {*t.pi}, "amount": {strconv.Itoa(cents)}, "reason": {"requested_by_customer"}}
	putMetadata(form, "metadata", s.metadata(t.accountID, r.TopupID))
	sctx, cancel := context.WithTimeout(ctx, stripeTO)
	defer cancel()
	// Keyed on what has been refunded so far: running the same command twice
	// before the webhook lands makes one refund, not two.
	re, err := s.stripe.CreateRefund(sctx, form, fmt.Sprintf("unify-compute:%s:refund:%s:%d:%d", s.env, r.TopupID, t.refunded, cents))
	if err != nil {
		return RefundIssued{}, fmt.Errorf("stripe refused or did not answer; nothing was refunded unless Stripe's dashboard shows it: %w", err)
	}
	if _, err := s.acc.Audit(ctx, accounts.AuditEvent{ActorType: accounts.ActorAdmin, ActorID: r.Actor, AccountID: t.accountID,
		Action: accounts.ActionRefund, Reason: r.Reason, Detail: map[string]any{"stage": "requested", "topup_id": r.TopupID,
			"refund": re.ID, "amount_usd": CentsUSD(cents), "status": re.Status}}); err != nil {
		return RefundIssued{RefundID: re.ID, Status: re.Status, AccountID: t.accountID, Cents: cents},
			fmt.Errorf("refund %s was created at Stripe, but its audit row was not written: %w", re.ID, err)
	}
	return RefundIssued{RefundID: re.ID, Status: re.Status, AccountID: t.accountID, Cents: cents}, nil
}
