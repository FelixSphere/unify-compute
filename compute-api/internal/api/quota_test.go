package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const testQuotas = `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,
  "max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"owners":{}}`

type usageFunc func(owner string) ([]store.SKUUsage, error)

func (f usageFunc) ActiveUsage(_ context.Context, owner string) ([]store.SKUUsage, error) {
	return f(owner)
}

func withQuota(t *testing.T, s *Server, u usageFunc) *Server {
	t.Helper()
	cfg, err := quota.Parse([]byte(testQuotas))
	if err != nil {
		t.Fatal(err)
	}
	s.Quota = &quota.Enforcer{Config: cfg, Catalogue: s.Catalogue, Usage: u}
	return s
}

func submitAs(t *testing.T, s *Server, owner, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/compute/v1/jobs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test-key")
	if owner != "" {
		req = req.WithContext(withOwner(req.Context(), owner))
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestSubmitOverQuotaIsRefusedWithHeadroom(t *testing.T) {
	var asked string
	s := withQuota(t, newTestServer(t, "aws"), func(owner string) ([]store.SKUUsage, error) {
		asked = owner
		return []store.SKUUsage{{SKU: "a10g-24gb-x1", Active: 2, Queued: 1}}, nil
	})
	rec := submitAs(t, s, "user:1042", validSubmit)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("a quota refusal must not carry Retry-After; when a slot frees is unknowable")
	}
	if asked != "user:1042" {
		t.Fatalf("usage read for %q, want the caller's owner", asked)
	}
	var body struct {
		Error struct {
			Type, Code, Message, Param string
			Quotas                     []quota.Violation
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	e := body.Error
	if e.Type != "quota_exceeded" || e.Code != "quota_exceeded" || e.Param != "sku" {
		t.Fatalf("error = %+v", e)
	}
	want := []quota.Violation{
		{Limit: quota.LimitActiveJobs, Max: 2, Used: 2, Requested: 1, Headroom: 0},
		{Limit: quota.LimitGPUs, Tier: "hyperscaler", Max: 2, Used: 2, Requested: 1, Headroom: 0},
	}
	if len(e.Quotas) != len(want) || e.Quotas[0] != want[0] || e.Quotas[1] != want[1] {
		t.Fatalf("quotas = %+v, want %+v", e.Quotas, want)
	}
	if !strings.Contains(e.Message, "headroom 0") || !strings.Contains(e.Message, "Nothing was submitted") {
		t.Fatalf("message: %s", e.Message)
	}
}

func TestSubmitWithinQuotaStillEndsInNotImplemented(t *testing.T) {
	s := withQuota(t, newTestServer(t, "aws"), func(string) ([]store.SKUUsage, error) {
		return []store.SKUUsage{{SKU: "a10g-24gb-x1", Active: 1}}, nil
	})
	if rec := submitAs(t, s, "user:1", validSubmit); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

// A malformed or unplaceable request is told what is wrong with it, not that
// the account is full; quota is the last check before the hold.
func TestRequestErrorsComeBeforeQuota(t *testing.T) {
	full := func(string) ([]store.SKUUsage, error) {
		return []store.SKUUsage{{SKU: "a10g-24gb-x1", Active: 2}}, nil
	}
	s := withQuota(t, newTestServer(t, "aws"), full)
	if rec := submitAs(t, s, "user:1", `{"sku":"nope","spec":{"max_runtime_seconds":60}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown sku on a full account: %d", rec.Code)
	}
	s = withQuota(t, newTestServer(t, ""), full)
	if rec := submitAs(t, s, "user:1", validSubmit); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no upstream on a full account: %d", rec.Code)
	}
}

func TestQuotaNeverSilentlySkipped(t *testing.T) {
	s := withQuota(t, newTestServer(t, "aws"), func(string) ([]store.SKUUsage, error) { return nil, nil })
	rec := submitAs(t, s, "", validSubmit)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "owner_unresolved") {
		t.Fatalf("no owner: %d %s", rec.Code, rec.Body.String())
	}

	s = withQuota(t, newTestServer(t, "aws"), func(string) ([]store.SKUUsage, error) { return nil, errors.New("db down") })
	rec = submitAs(t, s, "user:1", validSubmit)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "quota_check_failed") {
		t.Fatalf("usage read failed: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Fatal("internal error detail leaked to the customer")
	}
}

func TestSubmitsAreLimitedPerKey(t *testing.T) {
	s := newTestServer(t, "aws")
	for i := range submitBurst {
		if rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-rate-a", validSubmit); rec.Code != http.StatusNotImplemented {
			t.Fatalf("submit %d: %d", i+1, rec.Code)
		}
	}
	rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-rate-a", validSubmit)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("over budget: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"type":"rate_limited"`) {
		t.Fatalf("body: %s", rec.Body.String())
	}
	if rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-rate-b", validSubmit); rec.Code != http.StatusNotImplemented {
		t.Fatalf("another key shares the budget: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/compute/v1/jobs", "sk-rate-a", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("reads share the submit budget: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "", validSubmit); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous submit: %d, want 401 before any rate limit", rec.Code)
	}
}

func TestReadsAreLimitedPerKey(t *testing.T) {
	s := newTestServer(t, "aws")
	paths := []string{"/compute/v1/jobs", "/compute/v1/jobs/job_x", "/compute/v1/jobs/job_x/logs", "/compute/v1/jobs/job_x/artifacts"}
	for i := range readBurst {
		if rec := do(t, s, http.MethodGet, paths[i%len(paths)], "sk-reader", ""); rec.Code != http.StatusNotImplemented {
			t.Fatalf("read %d: %d", i+1, rec.Code)
		}
	}
	rec := do(t, s, http.MethodGet, "/compute/v1/jobs", "sk-reader", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("over budget: %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
