package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Reversal is money that came back out of, or back into, the balance at
// Stripe (task B2): a refund, a dispute's debit, or a won dispute's
// reinstatement. Quota is negative for refund and dispute, positive for
// dispute_reversal. topup_bonus is the share of a top-up's bonus credit that
// goes with any of them (ADR-0013): negative with a refund or dispute,
// positive with a won dispute.
type Reversal struct {
	AccountID int64
	Kind      string // refund, dispute, dispute_reversal or topup_bonus
	Quota     int64
	RefType   string
	RefID     string
	// Idempotency is the balance entry's key: refund:<topup>:<cumulative
	// cents>, dispute:<dp_…>, dispute_reversal:<dp_…>.
	Idempotency string
	Detail      any
}

// ReverseTx applies r within the caller's transaction, so the webhook
// commits the event, the top-up's state, the entry and the audit row
// together. Unlike an operator's debit it may take available below zero:
// Stripe has already moved the money, and the balance has to say so.
func (l *Local) ReverseTx(ctx context.Context, tx pgx.Tx, r Reversal) (Balance, error) {
	switch {
	case r.Kind == "refund" || r.Kind == "dispute":
		if r.Quota >= 0 {
			return Balance{}, errors.New("billing: a refund or dispute must debit")
		}
	case r.Kind == "dispute_reversal":
		if r.Quota <= 0 {
			return Balance{}, errors.New("billing: a dispute reversal must credit")
		}
	case r.Kind == "topup_bonus":
		if r.Quota == 0 {
			return Balance{}, errors.New("billing: a bonus reversal of zero")
		}
	default:
		return Balance{}, errors.New("billing: unknown reversal kind " + r.Kind)
	}
	a, err := lockAccount(ctx, tx, r.AccountID)
	if err != nil {
		return Balance{}, err
	}
	if err := l.move(ctx, tx, a, entry{kind: r.Kind, available: r.Quota, refType: r.RefType, refID: r.RefID,
		idemp: r.Idempotency, detail: r.Detail}); err != nil {
		return Balance{}, err
	}
	return Balance{Available: a.available, Held: a.held}, nil
}
