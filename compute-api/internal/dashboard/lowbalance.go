package dashboard

import (
	"errors"
	"net/http"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

type lowBalanceRequest struct {
	LowBalanceUSD *string `json:"low_balance_usd"`
}

// setLowBalance sets the account's low-balance warning threshold
// (accounts.md, "Low-balance warning"). No recent password is asked for:
// unlike a key's cap it controls only an email and a banner, not spending.
// Any user of the account may set it; there is only the owner today.
func (h *Handler) setLowBalance(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	var req lowBalanceRequest
	if !decode(w, r, &req) {
		return
	}
	if req.LowBalanceUSD == nil {
		writeAccountsError(w, accounts.ErrInvalidLowBalance)
		return
	}
	cents, ok := payments.ParseAmount(*req.LowBalanceUSD)
	if !ok || cents < 0 {
		writeAccountsError(w, accounts.ErrInvalidLowBalance)
		return
	}
	ctx := r.Context()
	a, err := h.acc.SetLowBalance(ctx, u.AccountID, int64(cents)*(sku.QuotaPerUSD/100))
	var ae *accounts.Error
	if errors.As(err, &ae) {
		writeAccountsError(w, ae)
		return
	}
	if err != nil {
		h.internal(w, "set low balance", err)
		return
	}
	v := viewBalance(a)
	h.audit(ctx, accounts.ActionLowBalanceChanged, clientInfo(r), u.ID, u.AccountID, map[string]any{"low_balance_usd": v.LowBalanceUSD})
	writeJSON(w, http.StatusOK, v)
}
