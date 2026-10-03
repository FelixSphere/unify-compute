package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

func shippedCeiling(t *testing.T) int64 {
	t.Helper()
	p, err := quota.LoadPlatform(filepath.Join("..", "..", "config", "platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p.MaxRuntimeSeconds
}

// R9 with the deployment's ceiling from config/platform.json (4 h while the
// platform starts small, ADR-0010): the same refusal, at the lower number,
// for a hand-written spec and for a template override alike.
func TestRuntimeCeilingComesFromPlatformConfig(t *testing.T) {
	ceiling := shippedCeiling(t)
	if ceiling != 4*3600 {
		t.Fatalf("shipped ceiling %d; ADR-0010 and upstreams.md R9 say 14400", ceiling)
	}
	s := newTemplateServer(t, "aws")
	s.RuntimeCeilingSeconds = ceiling
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"at the ceiling", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":14400}}`, 501, "not_implemented"},
		{"one over", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":14401}}`, 400, "runtime_over_ceiling"},
		{"the old 24 h", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":86400}}`, 400, "runtime_over_ceiling"},
		{"absurd", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":99999999999999}}`, 400, "runtime_over_ceiling"},
		{"template override at the ceiling", `{"template":"embed","spec":{` + in + `,"max_runtime_seconds":14400}}`, 501, "not_implemented"},
		{"template override over", `{"template":"embed","spec":{` + in + `,"max_runtime_seconds":14401}}`, 400, "runtime_over_ceiling"},
	}
	for _, tc := range cases {
		rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s: got %d %s, want %d %s", tc.name, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
		if tc.code == "runtime_over_ceiling" && !strings.Contains(rec.Body.String(), "ceiling of 14400") {
			t.Errorf("%s: message must name the deployment's ceiling: %s", tc.name, rec.Body.String())
		}
	}
}

// A ceiling above the contract cannot widen it.
func TestRuntimeCeilingNeverExceedsTheContract(t *testing.T) {
	s := newTestServer(t, "aws")
	s.RuntimeCeilingSeconds = 2 * MaxRuntimeSeconds
	rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":86401}}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "runtime_over_ceiling") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// main loads templates against the platform ceiling, so a template whose
// default runtime is over it fails the deploy. The shipped ones must fit.
func TestShippedTemplatesFitTheShippedCeiling(t *testing.T) {
	cat, err := sku.Load(filepath.Join("..", "..", "config", "skus.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := template.Load(filepath.Join("..", "..", "config", "templates.json"), cat, shippedCeiling(t)); err != nil {
		t.Fatal(err)
	}
}
