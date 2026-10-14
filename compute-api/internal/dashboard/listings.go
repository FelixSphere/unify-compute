package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

// listingWritesPerAccount bounds submits and edits: each one lands in the
// super admin's review queue.
var listingWritesPerAccount = accounts.Rule{Bucket: "listing_write_account", Limit: 20, Window: time.Hour}

// listingView is a listing as its publisher sees it (spec/marketplace.md
// M2): the template they submitted, where review stands, and how many
// jobs ran it. Never who ran them.
type listingView struct {
	ID           string            `json:"id"`
	Object       string            `json:"object"`
	TemplateID   string            `json:"template_id"`
	ReviewState  string            `json:"review_state"`
	ReviewReason string            `json:"review_reason,omitempty"`
	ReviewedAt   *int64            `json:"reviewed_at"`
	Listed       bool              `json:"listed"`
	Public       bool              `json:"public"`
	Runs         int64             `json:"runs"`
	Template     template.Template `json:"template"`
	CreatedAt    int64             `json:"created_at"`
	UpdatedAt    int64             `json:"updated_at"`
}

func viewListing(l listings.Listing) listingView {
	return listingView{ID: l.ID, Object: "compute.listing", TemplateID: l.TemplateID, ReviewState: l.ReviewState,
		ReviewReason: l.ReviewReason, ReviewedAt: unixPtr(l.ReviewedAt), Listed: l.Listed, Public: l.Public(),
		Runs: l.Runs, Template: l.Template, CreatedAt: l.CreatedAt.Unix(), UpdatedAt: l.UpdatedAt.Unix()}
}

type listingRequest struct {
	Template json.RawMessage `json:"template"`
}

func (h *Handler) listingsUnavailable(w http.ResponseWriter) bool {
	if h.listings == nil {
		notImplemented(w, "The template marketplace")
		return true
	}
	return false
}

func writeListingError(w http.ResponseWriter, err error) bool {
	var inv *listings.Invalid
	switch {
	case err == nil:
		return false
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "invalid_request", inv.Code, inv.Msg, inv.Field)
	case errors.Is(err, listings.ErrExists):
		writeError(w, http.StatusConflict, "conflict", "listing_exists", "A listing with this template id already exists.", "template.id")
	case errors.Is(err, listings.ErrNotFound):
		notFound(w)
	default:
		return false
	}
	return true
}

func (h *Handler) listListings(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.listingsUnavailable(w) {
		return
	}
	ls, err := h.listings.ByAccount(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "list listings", err)
		return
	}
	data := make([]listingView, len(ls))
	for i, l := range ls {
		data[i] = viewListing(l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "open": h.listings.Open()})
}

// createListing submits a template for review. The account must be able to
// run jobs: a listing is a job spec other accounts will run.
func (h *Handler) createListing(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.listingsUnavailable(w) {
		return
	}
	var req listingRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Template) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_listing", "Give the template to list, as one templates.json entry.", "template")
		return
	}
	ctx := r.Context()
	acct, err := h.acc.AccountByID(ctx, u.AccountID)
	if err != nil {
		h.internal(w, "account", err)
		return
	}
	if !acct.ComputeEnabled || acct.Status != accounts.AccountActive {
		writeError(w, http.StatusForbidden, "permission_error", "compute_not_enabled", "This account cannot run jobs yet, so it cannot list templates for others to run. Contact us.", "")
		return
	}
	// Validated before the throttle counts, so a typo does not use up the hour.
	if _, err := h.listings.Validate(req.Template); writeListingError(w, err) {
		return
	}
	if !h.hit(w, r, listingWritesPerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	l, err := h.listings.Create(ctx, u.AccountID, req.Template)
	if writeListingError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "create listing", err)
		return
	}
	h.audit(ctx, accounts.ActionListingSubmitted, clientInfo(r), u.ID, u.AccountID, map[string]any{"listing_id": l.ID, "template_id": l.TemplateID})
	writeJSON(w, http.StatusCreated, viewListing(l))
}

// updateListing replaces the template. The listing is relisted and returns
// to pending whatever changed: approving one version is not approving
// another.
func (h *Handler) updateListing(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.listingsUnavailable(w) {
		return
	}
	id := r.PathValue("id")
	var req listingRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Template) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_listing", "Give the whole template, as one templates.json entry.", "template")
		return
	}
	if _, err := h.listings.Validate(req.Template); writeListingError(w, err) {
		return
	}
	if !h.hit(w, r, listingWritesPerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	ctx := r.Context()
	l, err := h.listings.Update(ctx, u.AccountID, id, req.Template)
	if writeListingError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "update listing", err)
		return
	}
	h.audit(ctx, accounts.ActionListingUpdated, clientInfo(r), u.ID, u.AccountID, map[string]any{"listing_id": l.ID, "template_id": l.TemplateID})
	writeJSON(w, http.StatusOK, viewListing(l))
}

// unlistListing takes the listing off the market. Never refused for a
// listing the account owns; already unlisted answers the same 200.
func (h *Handler) unlistListing(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.listingsUnavailable(w) {
		return
	}
	ctx := r.Context()
	l, err := h.listings.Unlist(ctx, u.AccountID, r.PathValue("id"))
	if writeListingError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "unlist listing", err)
		return
	}
	h.audit(ctx, accounts.ActionListingUnlisted, clientInfo(r), u.ID, u.AccountID, map[string]any{"listing_id": l.ID, "template_id": l.TemplateID})
	writeJSON(w, http.StatusOK, viewListing(l))
}
