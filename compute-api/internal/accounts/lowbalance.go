package accounts

import (
	"context"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// MaxLowBalance bounds the low-balance threshold: USD 10,000, far above any
// balance the top-up limits allow, so any sensible value fits.
const MaxLowBalance = 10_000 * sku.QuotaPerUSD

var ErrInvalidLowBalance = &Error{"invalid_low_balance", "low_balance_usd",
	"low_balance_usd must be dollars with at most two decimals, from 0 (off) to 10000."}

// SetLowBalance sets the account's low-balance warning threshold, in quota
// units. The warning is armed again exactly when available is at or above
// the new value: lowering it under the balance means the next drop below it
// is emailed; raising it over the balance sends nothing now (the dashboard's
// banner says so) and waits for a top-up.
func (s *Service) SetLowBalance(ctx context.Context, accountID, quota int64) (Account, error) {
	if quota < 0 || quota > MaxLowBalance {
		return Account{}, ErrInvalidLowBalance
	}
	return scanAccount(s.db.QueryRow(ctx, `UPDATE accounts AS a SET low_balance_quota = $2, low_balance_armed = a.available >= $2
		WHERE a.id = $1 RETURNING `+accountColumns, accountID, quota))
}
