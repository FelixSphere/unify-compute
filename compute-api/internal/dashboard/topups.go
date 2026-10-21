package dashboard

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
)

// webhookPath is Stripe's endpoint for compute, on the app host:
// https://app.compute.unifyai.us/dashboard/v1/stripe/webhook.
const webhookPath = "/stripe/webhook"

// topupCreatePerAccount bounds Checkout Sessions per account. The daily
// limit counts only top-ups that were not abandoned, so without this a
// script could open sessions (and card attempts inside them) without end.
var topupCreatePerAccount = accounts.Rule{Bucket: "topup_create_account", Limit: 10, Window: time.Hour}

// refundRequestPerAccount is ADR-0013 §2's throttle: each request emails the
// operator.
var refundRequestPerAccount = accounts.Rule{Bucket: "refund_request_account", Limit: 5, Window: 24 * time.Hour}

type topupView struct {
	ID            string             `json:"id"`
	Object        string             `json:"object"`
	AmountUSD     string             `json:"amount_usd"`
	BonusUSD      string             `json:"bonus_usd"`
	Status        string             `json:"status"`
	CreatedAt     int64              `json:"created_at"`
	PaidAt        *int64             `json:"paid_at"`
	ReceiptURL    *string            `json:"receipt_url"`
	RefundedUSD   string             `json:"refunded_usd"`
	RefundableUSD string             `json:"refundable_usd"`
	RefundRequest *refundRequestView `json:"refund_request"`
}

type refundRequestView struct {
	ID         string `json:"id"`
	Object     string `json:"object"`
	TopupID    string `json:"topup_id"`
	AmountUSD  string `json:"amount_usd"`
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
	CreatedAt  int64  `json:"created_at"`
	ResolvedAt *int64 `json:"resolved_at"`
}

func viewRefundRequest(r payments.CustomerRequest) *refundRequestView {
	return &refundRequestView{ID: r.ID, Object: "compute.refund_request", TopupID: r.TopupID, AmountUSD: payments.CentsUSD(r.AmountCents),
		Status: r.Status, Resolution: r.Resolution, CreatedAt: r.CreatedAt.Unix(), ResolvedAt: unixPtr(r.ResolvedAt)}
}

func viewTopup(t payments.Topup) topupView {
	v := topupView{ID: t.ID, Object: "compute.topup", AmountUSD: payments.CentsUSD(t.AmountCents), BonusUSD: payments.CentsUSD(t.BonusCents),
		Status: t.Status, CreatedAt: t.CreatedAt.Unix(), PaidAt: unixPtr(t.PaidAt), RefundedUSD: payments.CentsUSD(t.RefundedCents),
		RefundableUSD: payments.CentsUSD(t.RefundableCents)}
	if t.ReceiptURL != "" {
		v.ReceiptURL = &t.ReceiptURL
	}
	if t.Request != nil {
		v.RefundRequest = viewRefundRequest(*t.Request)
	}
	return v
}

type bonusTierView struct {
	MinUSD  string `json:"min_usd"`
	Percent int    `json:"percent"`
}

func bonusTiers() []bonusTierView {
	out := make([]bonusTierView, len(payments.BonusTiers))
	for i, t := range payments.BonusTiers {
		out[i] = bonusTierView{MinUSD: payments.CentsUSD(t.MinCents), Percent: t.Percent}
	}
	return out
}

func writePaymentsError(w http.ResponseWriter, e *payments.Error) {
	if e.TopupID == "" {
		writeError(w, e.Status, e.Type, e.Code, e.Message, e.Param)
		return
	}
	writeJSON(w, e.Status, map[string]any{"error": map[string]string{"type": e.Type, "code": e.Code, "message": e.Message, "topup_id": e.TopupID}})
}

func (h *Handler) paymentsFailed(w http.ResponseWriter, what string, err error) {
	var pe *payments.Error
	if errors.As(err, &pe) {
		writePaymentsError(w, pe)
		return
	}
	h.internal(w, what, err)
}

type createTopupRequest struct {
	AmountUSD string `json:"amount_usd"`
}

// createTopup answers the Checkout URL the page sends the browser to. The
// balance changes only when Stripe's webhook says the payment succeeded.
// Owners and admins top up; members only spend.
func (h *Handler) createTopup(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.pay.Enabled() {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	var req createTopupRequest
	if !decode(w, r, &req) {
		return
	}
	if _, ok := payments.ParseAmount(req.AmountUSD); !ok {
		writePaymentsError(w, payments.ErrInvalidAmount)
		return
	}
	if !h.hit(w, r, topupCreatePerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	c, err := h.pay.CreateTopup(r.Context(), u, req.AmountUSD, clientInfo(r))
	if err != nil {
		h.paymentsFailed(w, "create top-up", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": c.ID, "object": "compute.topup", "status": c.Status, "checkout_url": c.CheckoutURL,
		"bonus_usd": payments.CentsUSD(c.BonusCents)})
}

// listTopups also says whether top-ups can be made in this deployment, so
// the page offers the form only when it would work.
func (h *Handler) listTopups(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.pay == nil {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	list, err := h.pay.ListTopups(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "list top-ups", err)
		return
	}
	data := make([]topupView, len(list))
	for i, t := range list {
		data[i] = viewTopup(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "has_more": false, "topups_enabled": h.pay.Enabled(),
		"bonus_tiers": bonusTiers()})
}

func (h *Handler) getTopup(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.pay == nil {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	t, err := h.pay.GetTopup(r.Context(), u.AccountID, r.PathValue("id"))
	if err != nil {
		h.paymentsFailed(w, "get top-up", err)
		return
	}
	writeJSON(w, http.StatusOK, viewTopup(t))
}

// stripeWebhook is Stripe's call, with the raw body as signed. 400 for a
// bad or stale signature (nothing stored), 200 for everything verified,
// including events that are not compute's, 500 when our database fails so
// Stripe retries.
func (h *Handler) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if h.pay == nil {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, payments.MaxWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed_body", "Body too large or unreadable.", "")
		return
	}
	outcome, err := h.pay.HandleWebhook(r.Context(), r.Header.Get("Stripe-Signature"), body)
	switch {
	case errors.Is(err, payments.ErrNoSignature), errors.Is(err, payments.ErrBadSignature), errors.Is(err, payments.ErrStaleSignature):
		h.log.Warn("stripe webhook refused", "err", err, "ip", clientIP(r))
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_signature", "Stripe-Signature does not verify.", "")
	case err != nil:
		h.paymentsFailed(w, "stripe webhook", err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "outcome": outcome})
	}
}

type refundRequestBody struct {
	AmountUSD string `json:"amount_usd"`
	Reason    string `json:"reason"`
}

// requestRefund asks the operator to refund a top-up to its card (ADR-0013
// §2). Nothing is refunded here. The same roles that can top up can ask.
func (h *Handler) requestRefund(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if h.pay == nil {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	var req refundRequestBody
	if !decode(w, r, &req) {
		return
	}
	if !h.hit(w, r, refundRequestPerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	rr, err := h.pay.CreateRefundRequest(r.Context(), u, r.PathValue("id"), req.AmountUSD, req.Reason, clientInfo(r))
	if err != nil {
		h.paymentsFailed(w, "request refund", err)
		return
	}
	writeJSON(w, http.StatusCreated, viewRefundRequest(rr))
}

func (h *Handler) cancelRefundRequest(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if h.pay == nil {
		writePaymentsError(w, payments.ErrUnavailable)
		return
	}
	if err := h.pay.CancelRefundRequest(r.Context(), u, r.PathValue("id"), clientInfo(r)); err != nil {
		h.paymentsFailed(w, "cancel refund request", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
