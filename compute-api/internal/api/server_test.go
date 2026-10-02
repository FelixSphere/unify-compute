package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

const testCatalogue = `{"note":"test","skus":[
  {"id":"a10g-24gb-x1","price_usd_per_hour":"1.2000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":1,
   "providers":[{"cloud":"aws","instance_type":"g5.xlarge","regions":["us-east-1"]},{"cloud":"gcp","instance_type":"g2-standard-4","regions":["us-central1"]}]},
  {"id":"l4-24gb-x1","price_usd_per_hour":"0.9000","available":false,"tier":"hyperscaler","accelerator":"L4",
   "providers":[{"cloud":"gcp","instance_type":"g2-standard-4"}]}
]}`

const validSubmit = `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":3600}}`

func newTestServer(t *testing.T, upstreams string) *Server {
	t.Helper()
	cat, err := sku.Parse([]byte(testCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := upstream.Parse(upstreams)
	if err != nil {
		t.Fatal(err)
	}
	return New("test", cat, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, s *Server, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestHealthNamesTheBuild(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodGet, "/healthz", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"build":"test"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSKUsArePublicAndCarryPlacement(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodGet, "/compute/v1/skus", "", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Data []sku.SKU `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 || body.Data[0].Object != "compute.sku" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
	a10g := body.Data[0]
	if a10g.Tier != sku.TierHyperscaler || len(a10g.Providers) != 2 || a10g.Accelerator != "A10G" {
		t.Fatalf("placement fields missing from the public SKU: %+v", a10g)
	}
}

func TestUpstreamsListCatalogueCloudsWithEnabledFlag(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodGet, "/compute/v1/upstreams", "", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Data []upstreamView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 || body.Data[0].Cloud != "aws" || !body.Data[0].Enabled || body.Data[1].Cloud != "gcp" || body.Data[1].Enabled {
		t.Fatalf("unexpected upstreams %s", rec.Body.String())
	}
	if len(body.Data[1].SKUs) != 2 {
		t.Fatalf("gcp should serve both SKUs, got %v", body.Data[1].SKUs)
	}
}

func TestJobRoutesRequireAKey(t *testing.T) {
	s := newTestServer(t, "aws")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/compute/v1/jobs"},
		{http.MethodGet, "/compute/v1/jobs"},
		{http.MethodGet, "/compute/v1/jobs/job_1"},
		{http.MethodPost, "/compute/v1/jobs/job_1/cancel"},
		{http.MethodGet, "/compute/v1/jobs/job_1/logs"},
		{http.MethodGet, "/compute/v1/jobs/job_1/artifacts"},
	} {
		rec := do(t, s, tc.method, tc.path, "", validSubmit)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without key: %d", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"type":"authentication_error"`) {
			t.Errorf("%s %s: body %s", tc.method, tc.path, rec.Body.String())
		}
		rec = do(t, s, tc.method, tc.path, "sk-abcdefghij", validSubmit)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s with key: %d, want 501 while the backend is unbuilt", tc.method, tc.path, rec.Code)
		}
	}
}

func TestMalformedKeysAreRejected(t *testing.T) {
	s := newTestServer(t, "aws")
	for _, key := range []string{"notakey", "sk-", "sk-1"} {
		if rec := do(t, s, http.MethodPost, "/compute/v1/jobs", key, validSubmit); rec.Code != 401 {
			t.Errorf("key %q: %d", key, rec.Code)
		}
	}
}

func TestSubmitValidatesBeforeNotImplemented(t *testing.T) {
	s := newTestServer(t, "aws")
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"malformed", `not json`, 400, "malformed_body"},
		{"missing sku", `{"spec":{"max_runtime_seconds":10}}`, 400, "missing_sku"},
		{"unknown sku", `{"sku":"h100-80gb-x1","spec":{"max_runtime_seconds":10}}`, 400, "unknown_sku"},
		{"unavailable sku", `{"sku":"l4-24gb-x1","spec":{"max_runtime_seconds":10}}`, 400, "sku_unavailable"},
		{"no runtime", `{"sku":"a10g-24gb-x1","spec":{}}`, 400, "missing_max_runtime"},
		{"over ceiling", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":86401}}`, 400, "runtime_over_ceiling"},
		{"provider not offered", `{"sku":"a10g-24gb-x1","providers":["azure"],"spec":{"max_runtime_seconds":10}}`, 400, "provider_not_offered"},
		{"provider offered but disabled", `{"sku":"a10g-24gb-x1","providers":["gcp"],"spec":{"max_runtime_seconds":10}}`, 503, "upstream_unavailable"},
		{"valid, aws only", `{"sku":"a10g-24gb-x1","providers":["aws"],"spec":{"max_runtime_seconds":86400}}`, 501, "not_implemented"},
		{"valid, any provider", validSubmit, 501, "not_implemented"},
	}
	for _, tc := range cases {
		rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: got %d %s, want %d %s", tc.name, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
	}
}

func TestEmptyRegistryRefusesEverySubmit(t *testing.T) {
	s := newTestServer(t, "")
	rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", validSubmit)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "upstream_unavailable") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
