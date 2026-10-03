package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

const testTemplates = `{"templates":[
  {"id":"embed","title":"Embed","description":"d",
   "image":"vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f",
   "image_licence":"Apache-2.0","command":["true"],"env":{"MODEL":"m"},"sku":"a10g-24gb-x1","max_runtime_seconds":600,
   "inputs":[{"mount":"/mnt/in","description":"requests"}],"outputs":[{"mount":"/mnt/out","description":"results"}],
   "overridable":["sku","providers","spec.env","spec.max_runtime_seconds"],"available":true},
  {"id":"on-unsold-sku","title":"T","description":"d",
   "image":"vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f",
   "command":["true"],"sku":"l4-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":[],"available":true},
  {"id":"placeholder","title":"T","description":"d","image":"","command":[],"sku":"a10g-24gb-x1","max_runtime_seconds":600,
   "inputs":[],"outputs":[],"overridable":[],"available":false,"unavailable_reason":"no image yet"}
]}`

const in = `"inputs":[{"source":"s3://acme/in/","mount":"/mnt/in"}]`

func newTemplateServer(t *testing.T, upstreams string) *Server {
	t.Helper()
	s := newTestServer(t, upstreams)
	set, err := template.Parse([]byte(testTemplates), s.Catalogue, MaxRuntimeSeconds)
	if err != nil {
		t.Fatal(err)
	}
	s.Templates = set
	return s
}

func TestTemplatesArePublic(t *testing.T) {
	rec := do(t, newTemplateServer(t, "aws"), http.MethodGet, "/compute/v1/templates", "", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Object string              `json:"object"`
		Data   []template.Template `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 3 || body.Data[0].ID != "embed" || body.Data[0].Object != "compute.template" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}
	if body.Data[1].Available || body.Data[1].UnavailableReason == "" {
		t.Fatalf("a template on an unsold sku must list as unavailable: %+v", body.Data[1])
	}
}

func TestTemplatesWithoutASetListEmpty(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodGet, "/compute/v1/templates", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestTemplateSubmitResolvesThenValidates(t *testing.T) {
	s := newTemplateServer(t, "aws")
	cases := []struct {
		name, body string
		status     int
		code       string
		param      string
	}{
		{"defaults only", `{"template":"embed","spec":{` + in + `}}`, 501, "not_implemented", ""},
		{"env and runtime overridden", `{"template":"embed","spec":{` + in + `,"env":{"MODEL":"x"},"max_runtime_seconds":86400}}`, 501, "not_implemented", ""},
		{"providers pinned", `{"template":"embed","providers":["aws"],"spec":{` + in + `}}`, 501, "not_implemented", ""},
		{"unknown template", `{"template":"nope","spec":{` + in + `}}`, 400, "unknown_template", "template"},
		{"template on unsold sku", `{"template":"on-unsold-sku"}`, 400, "template_unavailable", "template"},
		{"placeholder", `{"template":"placeholder"}`, 400, "template_unavailable", "template"},
		{"image override", `{"template":"embed","spec":{` + in + `,"image":"evil/image:1"}}`, 400, "field_not_overridable", "spec.image"},
		{"command override", `{"template":"embed","spec":{` + in + `,"command":["sh"]}}`, 400, "field_not_overridable", "spec.command"},
		{"outputs override", `{"template":"embed","spec":{` + in + `,"outputs":[{"mount":"/tmp"}]}}`, 400, "field_not_overridable", "spec.outputs"},
		{"missing input", `{"template":"embed"}`, 400, "missing_input", "spec.inputs"},
		{"undeclared mount", `{"template":"embed","spec":{"inputs":[{"source":"s3://a","mount":"/data"}]}}`, 400, "unknown_input_mount", "spec.inputs"},
		// From here the resolved spec meets the hand-written-spec checks.
		{"runtime override over ceiling", `{"template":"embed","spec":{` + in + `,"max_runtime_seconds":86401}}`, 400, "runtime_over_ceiling", "spec.max_runtime_seconds"},
		{"runtime override zero", `{"template":"embed","spec":{` + in + `,"max_runtime_seconds":0}}`, 400, "missing_max_runtime", "spec.max_runtime_seconds"},
		{"sku override unknown", `{"template":"embed","sku":"h100-80gb-x1","spec":{` + in + `}}`, 400, "unknown_sku", "sku"},
		{"sku override unsold", `{"template":"embed","sku":"l4-24gb-x1","spec":{` + in + `}}`, 400, "sku_unavailable", "sku"},
		{"sku override empty", `{"template":"embed","sku":"","spec":{` + in + `}}`, 400, "missing_sku", "sku"},
		{"provider not offered", `{"template":"embed","providers":["azure"],"spec":{` + in + `}}`, 400, "provider_not_offered", "providers"},
		{"provider disabled", `{"template":"embed","providers":["gcp"],"spec":{` + in + `}}`, 503, "upstream_unavailable", ""},
	}
	for _, tc := range cases {
		rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", tc.body)
		var body struct {
			Error apiError `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body.Error.Code != tc.code || body.Error.Param != tc.param {
			t.Errorf("%s: got %d %s, want %d %s param %q", tc.name, rec.Code, rec.Body.String(), tc.status, tc.code, tc.param)
		}
	}
}

func TestTemplateSubmitStillNeedsAKey(t *testing.T) {
	rec := do(t, newTemplateServer(t, "aws"), http.MethodPost, "/compute/v1/jobs", "", `{"template":"embed","spec":{`+in+`}}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestTemplateSubmitWithoutASetIsUnknown(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"embed"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"unknown_template"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestEmptyRegistryRefusesTemplateSubmitToo(t *testing.T) {
	rec := do(t, newTemplateServer(t, ""), http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"embed","spec":{`+in+`}}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
