package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
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
	return keyView{ID: k.ID, Name: k.Name, Display: k.Display, Scopes: k.Scopes, CreatedAt: k.CreatedAt.Unix(),
		ExpiresAt: unixPtr(k.ExpiresAt), LastUsedAt: unixPtr(k.LastUsedAt), RevokedAt: unixPtr(k.RevokedAt)}
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
	writeJSON(w, http.StatusOK, out)
}

type createKeyRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	// ExpiresAt is Unix seconds; absent or null never expires.
	ExpiresAt *int64 `json:"expires_at"`
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
	h.audit(ctx, accounts.ActionKeyCreated, clientInfo(r), u.ID, u.AccountID,
		map[string]any{"key_id": k.ID, "display": k.Display, "scopes": k.Scopes})
	mk := mailer.Key{Name: k.Name, Display: k.Display, Scopes: k.Scopes, CreatedAt: k.CreatedAt}
	h.sendLater("key created email", func(ctx context.Context) error {
		return h.mail.KeyCreated(ctx, u.Email, mk)
	})
	writeJSON(w, http.StatusCreated, createdKeyView{keyView: viewKey(k), Secret: secret})
}

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
