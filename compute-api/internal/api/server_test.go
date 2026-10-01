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
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cat, err := sku.Parse([]byte(`{"note":"test","skus":[{"id":"a10g-24gb-x1","price_usd_per_hour":"1.2000","available":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return New("test", cat, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, s *Server, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestHealthNamesTheBuild(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/healthz", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"build":"test"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestSKUsArePublic(t *testing.T) {
	rec := do(t, newTestServer(t), http.MethodGet, "/compute/v1/skus", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Data []sku.SKU `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Object != "compute.sku" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
}

func TestJobRoutesRequireAKey(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/compute/v1/jobs"},
		{http.MethodGet, "/compute/v1/jobs"},
		{http.MethodGet, "/compute/v1/jobs/job_1"},
		{http.MethodPost, "/compute/v1/jobs/job_1/cancel"},
		{http.MethodGet, "/compute/v1/jobs/job_1/logs"},
		{http.MethodGet, "/compute/v1/jobs/job_1/artifacts"},
	} {
		rec := do(t, s, tc.method, tc.path, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without key: %d", tc.method, tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"type":"authentication_error"`) {
			t.Errorf("%s %s: body %s", tc.method, tc.path, rec.Body.String())
		}
		rec = do(t, s, tc.method, tc.path, "sk-abcdefghij")
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s with key: %d, want 501 while the backend is unbuilt", tc.method, tc.path, rec.Code)
		}
	}
}

func TestMalformedKeysAreRejected(t *testing.T) {
	s := newTestServer(t)
	for _, key := range []string{"notakey", "sk-", "sk-1"} {
		if rec := do(t, s, http.MethodPost, "/compute/v1/jobs", key); rec.Code != 401 {
			t.Errorf("key %q: %d", key, rec.Code)
		}
	}
}
