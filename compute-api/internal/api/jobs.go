package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// MaxMetadata is job-api.md's limit on submit metadata pairs.
const MaxMetadata = 16

// wired serves h once the binary runs jobs, and 501 in the dark build.
func (s *Server) wired(what string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.JobService == nil || ownerFrom(r.Context()) == "" {
			s.notImplemented(what).ServeHTTP(w, r)
			return
		}
		h(w, r)
	})
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// submitWired is job-api.md's submit after the request's own validation:
// the spec must be launchable as given, placement must resolve, and then
// quota, the console hold, persistence, job.queued and the launch happen in
// that order inside jobs.Service.Submit. Nothing is held for a submit that
// fails any check before the hold, and nothing is persisted for one whose
// hold is refused.
func (s *Server) submitWired(w http.ResponseWriter, r *http.Request, sub submission) {
	c, ok := callerFrom(r.Context())
	if !ok {
		s.notImplemented("job submission").ServeHTTP(w, r)
		return
	}
	owner := c.owner
	if len(sub.metadata) > MaxMetadata {
		invalid(w, "invalid_metadata", "metadata", fmt.Sprintf("metadata has %d pairs; at most %d are allowed", len(sub.metadata), MaxMetadata))
		return
	}
	if !s.checkSpec(w, backend.Job{Spec: sub.spec, SKU: sub.item, Candidates: sub.item.Providers, Owner: owner}) {
		return
	}
	candidates, ok := s.place(w, sub)
	if !ok {
		return
	}
	if s.JobService.Backend == nil {
		writeError(w, http.StatusServiceUnavailable, apiError{Type: "upstream_unavailable", Code: "upstream_unavailable",
			Message: "This deployment has no SkyPilot API server configured, so no job can be launched."})
		return
	}
	var idem *store.Idempotency
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if len([]rune(key)) > 255 {
			invalid(w, "invalid_idempotency_key", "Idempotency-Key", "Idempotency-Key must be at most 255 characters")
			return
		}
		sum := sha256.Sum256(sub.raw)
		idem = &store.Idempotency{Key: key, RequestHash: sum[:]}
	}

	job, created, err := s.JobService.Submit(r.Context(), jobs.Submission{
		Owner: owner, UserID: c.billingID, TokenID: c.tokenID, SKU: sub.item, Candidates: candidates,
		Spec: sub.spec, Metadata: sub.metadata, Idempotency: idem,
	})
	var exceeded *quota.ExceededError
	var refused *ledger.InsufficientBalanceError
	var capped *billing.KeyCapError
	switch {
	case err == nil:
	case errors.As(err, &exceeded):
		writeError(w, http.StatusForbidden, apiError{Type: "quota_exceeded", Code: "quota_exceeded",
			Message: exceeded.Error(), Param: "sku", Quotas: exceeded.Violations})
		return
	case errors.As(err, &refused):
		writeError(w, http.StatusPaymentRequired, apiError{Type: "insufficient_balance", Code: "insufficient_balance",
			Message: fmt.Sprintf("The Unify API balance does not cover this job's hold of USD %s (up to %d s of %s at USD %s/h; the whole job can cost at most USD %s). Nothing was submitted; top up and retry.",
				quotaUSD(refused.HoldQuota), min(int64(sub.spec.MaxRuntimeSeconds), ledger.InitialHoldSeconds), sub.item.ID,
				sub.item.PriceUSDPerHour, quotaUSD(refused.CeilingQuota))})
		return
	case errors.As(err, &capped):
		writeError(w, http.StatusForbidden, apiError{Type: "permission_error", Code: "key_cap_exceeded", Param: "sku",
			Message: fmt.Sprintf("This API key's monthly cap of USD %s is reached: USD %s is spent or held this month, and this job would hold USD %s. Nothing was submitted; raise the cap in the dashboard or use another key.",
				quotaUSD(capped.Cap), quotaUSD(capped.Spent), quotaUSD(capped.Hold))})
		return
	case errors.Is(err, billing.ErrComputeNotEnabled):
		writeError(w, http.StatusForbidden, apiError{Type: "permission_error", Code: "compute_not_enabled",
			Message: "This account cannot run jobs yet. Contact us to have compute enabled."})
		return
	case errors.Is(err, billing.ErrAccountSuspended):
		writeError(w, http.StatusForbidden, apiError{Type: "authentication_error", Code: "account_suspended",
			Message: "This account is suspended and cannot submit jobs. Contact us."})
		return
	case errors.Is(err, jobs.ErrConsoleUnavailable):
		writeError(w, http.StatusServiceUnavailable, apiError{Type: "console_unavailable", Code: "console_unavailable",
			Message: "The balance hold could not be placed with Unify API right now. Nothing was submitted; retry shortly."})
		return
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, apiError{Type: "conflict", Code: "idempotency_conflict", Param: "Idempotency-Key",
			Message: "This Idempotency-Key was already used with a different request body."})
		return
	case errors.Is(err, quota.ErrUnknownActiveSKU):
		s.Log.Error("quota check", "owner", owner, "err", err)
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "quota_check_failed",
			Message: "the quota check could not be completed; nothing was submitted"})
		return
	default:
		s.Log.Error("submit", "owner", owner, "err", err)
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error",
			Message: "the job could not be submitted; nothing was charged"})
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	s.writeJob(w, r.Context(), status, job)
}

// checkSpec refuses a spec the backend could not launch, before anything
// is held: the same translation the launch does, with this deployment's
// options.
func (s *Server) checkSpec(w http.ResponseWriter, j backend.Job) bool {
	err := s.JobService.CheckSpec(j)
	switch {
	case err == nil:
		return true
	case errors.Is(err, backend.ErrNoImage):
		invalid(w, "missing_image", "spec.image", "spec.image is required")
	case errors.Is(err, backend.ErrNoCommand):
		invalid(w, "missing_command", "spec.command", "spec.command is required: the full argv to run in the image")
	case errors.Is(err, backend.ErrBadEnv):
		invalid(w, "invalid_env", "spec.env", err.Error())
	case errors.Is(err, backend.ErrReservedEnv):
		invalid(w, "reserved_env", "spec.env", err.Error())
	case errors.Is(err, backend.ErrInputSource):
		invalid(w, "invalid_input_source", "spec.inputs", "every spec.inputs[].source must be an https:// URL ending in a file name; s3:// and other sources are not supported yet")
	case errors.Is(err, backend.ErrBadMount), errors.Is(err, artifacts.ErrMountOverlap):
		invalid(w, "invalid_mount", "spec", err.Error())
	case errors.Is(err, backend.ErrOutputsUnsupported):
		writeError(w, http.StatusNotImplemented, apiError{Type: "not_implemented", Code: "outputs_unsupported", Param: "spec.outputs",
			Message: "spec.outputs cannot be stored by this deployment yet: artifact storage is not configured."})
	default:
		s.Log.Error("spec check", "err", err)
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error", Message: "the spec could not be checked"})
	}
	return false
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.JobService.Store.Get(r.Context(), ownerFrom(r.Context()), id)
	if errors.Is(err, store.ErrNotFound) {
		jobNotFound(w, id)
		return
	}
	if err != nil {
		s.internal(w, "get job", err)
		return
	}
	s.writeJob(w, r.Context(), http.StatusOK, job)
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := store.ListParams{Owner: ownerFrom(r.Context()), After: q.Get("after")}
	for _, v := range q["status"] {
		st := store.Status(v)
		if !st.Valid() {
			invalid(w, "invalid_status", "status", fmt.Sprintf("status %q is not a job status", v))
			return
		}
		p.Statuses = append(p.Statuses, st)
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxListLimit {
			invalid(w, "invalid_limit", "limit", fmt.Sprintf("limit must be an integer from 1 to %d", store.MaxListLimit))
			return
		}
		p.Limit = n
	}
	page, err := s.JobService.Store.List(r.Context(), p)
	if errors.Is(err, store.ErrInvalidCursor) {
		invalid(w, "invalid_cursor", "after", "after must be the id of one of your jobs")
		return
	}
	if err != nil {
		s.internal(w, "list jobs", err)
		return
	}
	data := make([]jobView, 0, len(page.Jobs))
	for _, j := range page.Jobs {
		v, err := s.view(r.Context(), j)
		if err != nil {
			s.internal(w, "list jobs", err)
			return
		}
		data = append(data, v)
	}
	out := map[string]any{"object": "list", "data": data, "has_more": page.HasMore}
	if page.HasMore {
		out["next"] = page.Next
	}
	writeJSON(w, http.StatusOK, out)
}

// cancelJob answers 202 once the cancel is accepted; GPU metering stops at
// that moment (job-api.md), while teardown runs on.
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, err := s.JobService.Cancel(r.Context(), ownerFrom(r.Context()), id, s.now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		jobNotFound(w, id)
	case errors.Is(err, jobs.ErrTerminal):
		writeError(w, http.StatusConflict, apiError{Type: "conflict", Code: "job_terminal",
			Message: fmt.Sprintf("job %s has already ended (%s)", id, job.Status)})
	case err != nil:
		s.internal(w, "cancel job", err)
	default:
		s.writeJob(w, r.Context(), http.StatusAccepted, job)
	}
}

// jobLogs returns what the job has printed so far. Logs are read from the
// cluster, so a job that has been torn down, or not launched yet, returns an
// empty body (backend.md open question 4: logs are not kept after teardown
// yet).
func (s *Server) jobLogs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("follow") == "true" {
		s.notImplemented("log streaming (follow=true)").ServeHTTP(w, r)
		return
	}
	id := r.PathValue("id")
	var buf bytes.Buffer
	err := s.JobService.Logs(r.Context(), ownerFrom(r.Context()), id, &buf)
	switch {
	case errors.Is(err, store.ErrNotFound):
		jobNotFound(w, id)
		return
	case err != nil:
		s.Log.Error("job logs", "job", id, "err", err)
		writeError(w, http.StatusServiceUnavailable, apiError{Type: "upstream_unavailable", Code: "logs_unavailable",
			Message: "The job's logs could not be read from its cluster right now; retry shortly."})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.Log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error", Message: what + " failed"})
}

// jobView is the Job object (job-api.md, "Objects").
type jobView struct {
	ID         string            `json:"id"`
	Object     string            `json:"object"`
	Status     store.Status      `json:"status"`
	SKU        string            `json:"sku"`
	CreatedAt  int64             `json:"created_at"`
	StartedAt  *int64            `json:"started_at"`
	FinishedAt *int64            `json:"finished_at"`
	Spec       json.RawMessage   `json:"spec"`
	Usage      usageView         `json:"usage"`
	Error      *store.JobError   `json:"error"`
	Placement  json.RawMessage   `json:"placement"`
	Metadata   map[string]string `json:"metadata"`
}

type usageView struct {
	GPUSeconds       int64  `json:"gpu_seconds"`
	EstimatedCostUSD string `json:"estimated_cost_usd"`
	SettledCostUSD   string `json:"settled_cost_usd"`
	HoldUSD          string `json:"hold_usd"`
}

func unix(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.Unix()
	return &v
}

func (s *Server) view(ctx context.Context, j store.Job) (jobView, error) {
	u, err := s.JobService.Usage(ctx, j)
	if err != nil {
		return jobView{}, err
	}
	estimate := "0.0000"
	if u.RateUSDPerHour != "" {
		if e, err := (sku.SKU{PriceUSDPerHour: u.RateUSDPerHour}).CostUSD(u.MaxRuntimeSeconds); err == nil {
			estimate = e
		}
	}
	meta := j.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	return jobView{
		ID: j.ID, Object: "compute.job", Status: j.Status, SKU: j.SKU,
		CreatedAt: j.CreatedAt.Unix(), StartedAt: unix(j.StartedAt), FinishedAt: unix(j.FinishedAt),
		Spec: j.Spec, Error: j.Error, Placement: j.Placement, Metadata: meta,
		Usage: usageView{GPUSeconds: u.GPUSeconds, EstimatedCostUSD: estimate,
			SettledCostUSD: quotaUSD(u.SettledQuota), HoldUSD: quotaUSD(u.HoldQuota)},
	}, nil
}

func (s *Server) writeJob(w http.ResponseWriter, ctx context.Context, status int, j store.Job) {
	v, err := s.view(ctx, j)
	if err != nil {
		s.internal(w, "render job", err)
		return
	}
	writeJSON(w, status, v)
}

// quotaUSD renders console quota units as USD with 4 decimals; the console
// unit is 1/500000 USD (console-billing-bridge.md, "Quota unit").
func quotaUSD(q int64) string {
	return new(big.Rat).SetFrac64(q, sku.QuotaPerUSD).FloatString(4)
}
