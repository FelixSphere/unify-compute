package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

// fakeListings stands in for the marketplace's account side: approved
// listings as templates, resolvable by id.
type fakeListings struct {
	approved []template.Template
	fail     error
}

func (f *fakeListings) Approved(context.Context) ([]template.Template, error) {
	return f.approved, f.fail
}

func (f *fakeListings) Template(_ context.Context, id string) (template.Template, error) {
	if f.fail != nil {
		return template.Template{}, f.fail
	}
	for _, t := range f.approved {
		if t.ID == id {
			return t, nil
		}
	}
	return template.Template{}, template.ErrUnknown
}

func newListingServer(t *testing.T, fail error) *Server {
	t.Helper()
	s := newTemplateServer(t, "aws")
	set, err := template.Parse([]byte(`{"templates":[{"id":"acme-embed","title":"Acme","description":"d",
	  "image":"vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f",
	  "command":["true"],"sku":"a10g-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":["sku"],"available":true}]}`),
		s.Catalogue, MaxRuntimeSeconds)
	if err != nil {
		t.Fatal(err)
	}
	approved := set.List()
	approved[0].Publisher = template.PublisherOfRecord
	s.Listings = &fakeListings{approved: approved, fail: fail}
	return s
}

func TestTemplatesListApprovedListingsAfterThePlatformOnesUnderOnePublisher(t *testing.T) {
	rec := do(t, newListingServer(t, nil), http.MethodGet, "/compute/v1/templates", "", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Data []template.Template `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 4 || body.Data[3].ID != "acme-embed" || body.Data[0].ID != "embed" {
		t.Fatalf("%+v", body.Data)
	}
	for _, tpl := range body.Data {
		if tpl.Publisher != template.PublisherOfRecord {
			t.Errorf("%s: publisher %q; nothing about the seller crosses", tpl.ID, tpl.Publisher)
		}
	}
}

func TestPlatformTemplatesCarryThePublisherWithoutAMarketplace(t *testing.T) {
	rec := do(t, newTemplateServer(t, "aws"), http.MethodGet, "/compute/v1/templates", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"publisher":"Unify Compute"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSubmitResolvesAnApprovedListingLikeAPlatformTemplate(t *testing.T) {
	s := newListingServer(t, nil)
	rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"acme-embed"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("a valid listing submit must reach the backend gate: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"acme-embed","spec":{"image":"evil/image:1"}}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"field_not_overridable"`) {
		t.Fatalf("listing overrides are enforced like a platform template's: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"acme-nope"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"code":"unknown_template"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAFailingMarketplaceDoesNotHideThePlatformTemplatesFromASubmit(t *testing.T) {
	s := newListingServer(t, errors.New("db down"))
	rec := do(t, s, http.MethodGet, "/compute/v1/templates", "", "")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"embed","spec":{`+in+`}}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("platform template must resolve without the marketplace: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"template":"acme-embed"}`)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"template_resolution_failed"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
