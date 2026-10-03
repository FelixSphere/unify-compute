package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
)

// The session routes over the account's money and jobs (accounts.md,
// "Session"): the balance, its history, monthly usage, and the job API's
// job routes for the session's account.

// Reads are limited per account like the job API's reads are per key (600 a
// minute, job-api.md "Rate limits"), in process memory: a page polling a
// running job stays far below it, a script looping on a stolen cookie does
// not.
const (
	readBurst  = 600
	readWindow = time.Minute

	defaultHistoryLimit = 20
	maxHistoryLimit     = 100
)

// JobRoutes is the job API's job handlers, served for an account the
// session resolved (api.AccountJobs). Each reads {id} from the request's own
// path, which this package's patterns name the same way.
type JobRoutes interface {
	List(w http.ResponseWriter, r *http.Request, accountID int64)
	Get(w http.ResponseWriter, r *http.Request, accountID int64)
	Cancel(w http.ResponseWriter, r *http.Request, accountID int64)
	Logs(w http.ResponseWriter, r *http.Request, accountID int64)
	Events(w http.ResponseWriter, r *http.Request, accountID int64)
	Artifacts(w http.ResponseWriter, r *http.Request, accountID int64)
}

// Ledger is what /transactions and /usage read (billing.Local).
type Ledger interface {
	History(ctx context.Context, accountID int64, after string, limit int) (billing.HistoryPage, error)
	Usage(ctx context.Context, accountID int64, from, to time.Time) ([]billing.SKUUsage, error)
}

func notImplemented(w http.ResponseWriter, what string) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "not_implemented",
		what+" is not available in this deployment yet.", "")
}

// read spends one of the account's read tokens.
func (h *Handler) read(next sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
		if ok, wait := h.reads.Allow("account:" + strconv.FormatInt(u.AccountID, 10)); !ok {
			secs := max(int64((wait+time.Second-1)/time.Second), 1)
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate_limited",
				fmt.Sprintf("Too many requests. Try again in %s.", humanWait(secs)), "")
			return
		}
		next(w, r, s, u)
	}
}

func (h *Handler) job(route func(JobRoutes, http.ResponseWriter, *http.Request, int64)) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
		if h.jobs == nil {
			notImplemented(w, "Jobs in the dashboard are")
			return
		}
		route(h.jobs, w, r, u.AccountID)
	}
}

func centsUSD(c int) string { return new(big.Rat).SetFrac64(int64(c), 100).FloatString(2) }

type balanceView struct {
	Object       string       `json:"object"`
	Currency     string       `json:"currency"`
	AvailableUSD string       `json:"available_usd"`
	HeldUSD      string       `json:"held_usd"`
	Limits       balanceLimit `json:"limits"`
}

type balanceLimit struct {
	TopupMinUSD   string `json:"topup_min_usd"`
	TopupMaxUSD   string `json:"topup_max_usd"`
	BalanceMaxUSD string `json:"balance_max_usd"`
	TopupsPerDay  int    `json:"topups_per_day"`
}

func (h *Handler) balance(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	a, err := h.acc.AccountByID(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "balance", err)
		return
	}
	writeJSON(w, http.StatusOK, balanceView{Object: "compute.balance", Currency: "USD",
		AvailableUSD: billing.USD(a.Available), HeldUSD: billing.USD(a.Held),
		Limits: balanceLimit{TopupMinUSD: centsUSD(a.TopupMinCents), TopupMaxUSD: centsUSD(a.TopupMaxCents),
			BalanceMaxUSD: centsUSD(a.BalanceMaxCents), TopupsPerDay: a.TopupsPerDay}})
}

type entryRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type entryView struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Kind              string            `json:"kind"`
	AmountUSD         string            `json:"amount_usd"`
	AvailableDeltaUSD string            `json:"available_delta_usd"`
	HeldDeltaUSD      string            `json:"held_delta_usd"`
	AvailableAfterUSD string            `json:"available_after_usd"`
	HeldAfterUSD      string            `json:"held_after_usd"`
	JobID             *string           `json:"job_id"`
	Ref               *entryRef         `json:"ref"`
	CreatedAt         int64             `json:"created_at"`
	Detail            map[string]string `json:"detail"`
}

// viewEntry is what the account holder sees of an entry. amount_usd is the
// one number a history row shows: what a settle charged out of held, and for
// every other kind the change in available. The ref is the job (or top-up)
// the entry belongs to; a hold id means nothing to a customer, and an
// operator adjustment's ref is an audit event whose reason is internal, so
// neither is shown.
func viewEntry(e billing.HistoryEntry) entryView {
	amount := e.AvailableDelta
	if amount == 0 {
		amount = e.HeldDelta
	}
	v := entryView{ID: e.ID, Object: "compute.balance_entry", Kind: e.Kind, AmountUSD: billing.USD(amount),
		AvailableDeltaUSD: billing.USD(e.AvailableDelta), HeldDeltaUSD: billing.USD(e.HeldDelta),
		AvailableAfterUSD: billing.USD(e.AvailableAfter), HeldAfterUSD: billing.USD(e.HeldAfter),
		CreatedAt: e.CreatedAt.Unix(), Detail: map[string]string{}}
	switch {
	case e.JobID != "":
		v.JobID, v.Ref = &e.JobID, &entryRef{Type: "job", ID: e.JobID}
	case e.RefType == "job" || e.RefType == "topup":
		v.Ref = &entryRef{Type: e.RefType, ID: e.RefID}
		if e.RefType == "job" {
			v.JobID = &e.RefID
		}
	}
	if e.SKU != "" {
		v.Detail["sku"] = e.SKU
	}
	return v
}

func (h *Handler) transactions(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.ledger == nil {
		notImplemented(w, "The balance history is")
		return
	}
	q := r.URL.Query()
	limit := defaultHistoryLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxHistoryLimit {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid_limit",
				fmt.Sprintf("limit must be an integer from 1 to %d.", maxHistoryLimit), "limit")
			return
		}
		limit = n
	}
	page, err := h.ledger.History(r.Context(), u.AccountID, q.Get("after"), limit)
	if errors.Is(err, billing.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_cursor", "after must be the id of an entry in your balance history.", "after")
		return
	}
	if err != nil {
		h.internal(w, "balance history", err)
		return
	}
	data := make([]entryView, len(page.Entries))
	for i, e := range page.Entries {
		data[i] = viewEntry(e)
	}
	out := map[string]any{"object": "list", "data": data, "has_more": page.HasMore}
	if page.HasMore {
		out["next"] = data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, out)
}

type skuUsageView struct {
	SKU        string `json:"sku"`
	GPUSeconds int64  `json:"gpu_seconds"`
	USD        string `json:"usd"`
}

type usageView struct {
	Object     string         `json:"object"`
	Month      string         `json:"month"`
	Currency   string         `json:"currency"`
	GPUSeconds int64          `json:"gpu_seconds"`
	GPUUSD     string         `json:"gpu_usd"`
	StorageUSD string         `json:"storage_usd"`
	EgressUSD  string         `json:"egress_usd"`
	TotalUSD   string         `json:"total_usd"`
	BySKU      []skuUsageView `json:"by_sku"`
}

// Holds that are not a job's GPU hold carry the meter as their SKU
// (accounts.md, "Local holds"; metering.md R5).
const (
	meterStorage = "storage"
	meterEgress  = "egress"
)

// usage is one UTC calendar month of settled charges. Totals are summed in
// quota units and converted once, so total_usd is exactly the sum of what
// the month's settle entries took, never a sum of rounded strings.
func (h *Handler) usage(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.ledger == nil {
		notImplemented(w, "Usage is")
		return
	}
	month := r.URL.Query().Get("month")
	var from time.Time
	if month == "" {
		now := h.now().UTC()
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	} else {
		t, err := time.Parse("2006-01", month)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid_month", "month must be a UTC calendar month, YYYY-MM.", "month")
			return
		}
		from = t
	}
	rows, err := h.ledger.Usage(r.Context(), u.AccountID, from, from.AddDate(0, 1, 0))
	if err != nil {
		h.internal(w, "usage", err)
		return
	}
	var gpu, storage, egress, secs int64
	bySKU := []skuUsageView{}
	for _, row := range rows {
		switch row.SKU {
		case meterStorage:
			storage += row.Quota
		case meterEgress:
			egress += row.Quota
		default:
			gpu += row.Quota
			secs += row.GPUSeconds
			bySKU = append(bySKU, skuUsageView{SKU: row.SKU, GPUSeconds: row.GPUSeconds, USD: billing.USD(row.Quota)})
		}
	}
	writeJSON(w, http.StatusOK, usageView{Object: "compute.usage", Month: from.Format("2006-01"), Currency: "USD",
		GPUSeconds: secs, GPUUSD: billing.USD(gpu), StorageUSD: billing.USD(storage), EgressUSD: billing.USD(egress),
		TotalUSD: billing.USD(gpu + storage + egress), BySKU: bySKU})
}
