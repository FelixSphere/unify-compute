package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// keyCreatePerAccount bounds key creation, which sends an email each time:
// revoking and re-creating cannot be turned into a mail cannon aimed at the
// account's own address.
var keyCreatePerAccount = accounts.Rule{Bucket: "key_create_account", Limit: 20, Window: time.Hour}

// keyView is a key as GET /keys lists it (spec/accounts.md). Times are Unix
// seconds; the nullable ones are null when unset.
type keyView struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Display    string   `json:"display"`
	Scopes     []string `json:"scopes"`
	CreatedAt  int64    `json:"created_at"`
	ExpiresAt  *int64   `json:"expires_at"`
	LastUsedAt *int64   `json:"last_used_at"`
	RevokedAt  *int64   `json:"revoked_at"`
	// MonthlyCapUSD is null for no cap. SpentThisMonthUSD is what the key's
	// holds this UTC month amount to (open at full quota, closed at what they
	// settled), the figure the cap is checked against; null when this
	// deployment has no local ledger.
	MonthlyCapUSD     *string `json:"monthly_cap_usd"`
	SpentThisMonthUSD *string `json:"spent_this_month_usd"`
}

// createdKeyView is the one response that carries the secret.
type createdKeyView struct {
	keyView
	Secret string `json:"secret"`
}

func unixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

func viewKey(k accounts.APIKey) keyView {
	v := keyView{ID: k.ID, Name: k.Name, Display: k.Display, Scopes: k.Scopes, CreatedAt: k.CreatedAt.Unix(),
		ExpiresAt: unixPtr(k.ExpiresAt), LastUsedAt: unixPtr(k.LastUsedAt), RevokedAt: unixPtr(k.RevokedAt)}
	if k.MonthlyCap != nil {
		usd := billing.USD(*k.MonthlyCap)
		v.MonthlyCapUSD = &usd
	}
	return v
}

// capQuota turns the dollars a customer typed into quota units: at most two
// decimals, 0.01 to 1 000 000, like a top-up amount. nil clears the cap.
func capQuota(usd *string) (*int64, bool) {
	if usd == nil {
		return nil, true
	}
	cents, ok := payments.ParseAmount(*usd)
	if !ok || cents <= 0 || cents > 1_000_000*100 {
		return nil, false
	}
	q := int64(cents) * (sku.QuotaPerUSD / 100)
	return &q, true
}

type updateKeyRequest struct {
	MonthlyCapUSD *string `json:"monthly_cap_usd"`
}

// updateKey sets or clears a key's monthly cap. Like creation it needs a
// recent password, because a cap is a spending control.
func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if !s.ReauthFresh(h.acc.Now()) {
		writeAccountsError(w, errReauthRequired)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		notFound(w)
		return
	}
	var req updateKeyRequest
	if !decode(w, r, &req) {
		return
	}
	q, ok := capQuota(req.MonthlyCapUSD)
	if !ok {
		writeAccountsError(w, accounts.ErrInvalidCap)
		return
	}
	ctx := r.Context()
	k, err := h.acc.SetKeyCap(ctx, u.AccountID, id, q)
	if errors.Is(err, accounts.ErrNotFound) {
		notFound(w)
		return
	}
	var ae *accounts.Error
	if errors.As(err, &ae) {
		writeAccountsError(w, ae)
		return
	}
	if err != nil {
		h.internal(w, "update key", err)
		return
	}
	detail := map[string]any{"key_id": k.ID, "display": k.Display}
	if k.MonthlyCap != nil {
		detail["monthly_cap"] = *k.MonthlyCap
	}
	h.audit(ctx, accounts.ActionKeyCapChanged, clientInfo(r), u.ID, u.AccountID, detail)
	writeJSON(w, http.StatusOK, h.withSpend(ctx, u.AccountID, []keyView{viewKey(k)})[0])
}

// withSpend fills spent_this_month_usd from the ledger, when there is one.
func (h *Handler) withSpend(ctx context.Context, accountID int64, views []keyView) []keyView {
	if h.ledger == nil {
		return views
	}
	spend, err := h.ledger.KeySpend(ctx, accountID, monthStart(h.now()))
	if err != nil {
		// The list is still right without it; the figure is informational.
		h.log.Warn("key spend", "err", err)
		return views
	}
	for i := range views {
		usd := billing.USD(spend[views[i].ID])
		views[i].SpentThisMonthUSD = &usd
	}
	return views
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	keys, err := h.acc.ListKeys(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "list keys", err)
		return
	}
	out := make([]keyView, len(keys))
	for i, k := range keys {
		out[i] = viewKey(k)
	}
	writeJSON(w, http.StatusOK, h.withSpend(r.Context(), u.AccountID, out))
}

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// ExpiresAt is Unix seconds; absent or null never expires.
	ExpiresAt *int64 `json:"expires_at"`
	// MonthlyCapUSD is dollars with at most two decimals; absent or null is
	// no cap.
	MonthlyCapUSD *string `json:"monthly_cap_usd"`
}

var errReauthRequired = &accounts.Error{Code: "reauth_required", Message: "Enter your password again to create an API key."}

// createKey needs the password entered within accounts.ReauthWindow in this
// session (ADR-0011 §8), checked before the body so a stale session learns
// that first. The secret is in this response and nowhere else, ever. The
// account is told by email, so a key minted with a stolen session does not
// go unnoticed.
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if !s.ReauthFresh(h.acc.Now()) {
		writeAccountsError(w, errReauthRequired)
		return
	}
	var req createKeyRequest
	if !decode(w, r, &req) {
		return
	}
	var expires *time.Time
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		expires = &t
	}
	// Validated before the throttle counts, so a typo does not use up the
	// hour; CreateKey validates again.
	if _, err := accounts.ValidateKeyName(req.Name); err != nil {
		writeAccountsError(w, accounts.ErrInvalidKeyName)
		return
	}
	if _, err := accounts.NormalizeScopes(req.Scopes); err != nil {
		writeAccountsError(w, accounts.ErrInvalidScope)
		return
	}
	capQ, ok := capQuota(req.MonthlyCapUSD)
	if !ok {
		writeAccountsError(w, accounts.ErrInvalidCap)
		return
	}
	if !h.hit(w, r, keyCreatePerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	ctx := r.Context()
	k, secret, err := h.acc.CreateKey(ctx, u.AccountID, u.ID, req.Name, req.Scopes, expires)
	var ae *accounts.Error
	if errors.As(err, &ae) {
		writeAccountsError(w, ae)
		return
	}
	if err != nil {
		h.internal(w, "create key", err)
		return
	}
	if capQ != nil {
		if k, err = h.acc.SetKeyCap(ctx, u.AccountID, k.ID, capQ); err != nil {
			h.internal(w, "set key cap", err)
			return
		}
	}
	h.audit(ctx, accounts.ActionKeyCreated, clientInfo(r), u.ID, u.AccountID,
		map[string]any{"key_id": k.ID, "display": k.Display, "scopes": k.Scopes, "monthly_cap": k.MonthlyCap})
	mk := mailer.Key{Name: k.Name, Display: k.Display, Scopes: k.Scopes, CreatedAt: k.CreatedAt}
	h.sendLater("key created email", func(ctx context.Context) error {
		return h.mail.KeyCreated(ctx, u.Email, mk)
	})
	writeJSON(w, http.StatusCreated, createdKeyView{keyView: viewKey(k), Secret: secret})
}

var errRevokeOthers = &accounts.Error{Code: "role_required", Message: "Members can revoke only the API keys they created. Ask an owner or admin to revoke this one."}

// revokeKey takes effect on the job API's next request: keys are looked up
// on every request, never cached. Revoking an already revoked key is a 204
// that changes nothing; another account's key is 404, like a missing one.
func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w)
		return
	}
	ctx := r.Context()
	// A member revokes only the keys it created; owners and admins any.
	if !accounts.CanManage(u.Role) {
		k, err := h.acc.KeyByID(ctx, u.AccountID, id)
		if errors.Is(err, accounts.ErrNotFound) {
			notFound(w)
			return
		}
		if err != nil {
			h.internal(w, "load key", err)
			return
		}
		if k.CreatedBy != u.ID {
			writeAccountsError(w, errRevokeOthers)
			return
		}
	}
	k, changed, err := h.acc.RevokeKey(ctx, u.AccountID, id)
	if errors.Is(err, accounts.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internal(w, "revoke key", err)
		return
	}
	if changed {
		h.audit(ctx, accounts.ActionKeyRevoked, clientInfo(r), u.ID, u.AccountID,
			map[string]any{"key_id": k.ID, "display": k.Display})
	}
	w.WriteHeader(http.StatusNoContent)
}
