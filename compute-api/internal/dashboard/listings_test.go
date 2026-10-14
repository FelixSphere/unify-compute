package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

const listingCatalogue = `{"note":"test","skus":[
  {"id":"a10g-24gb-x1","price_usd_per_hour":"1.2000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":1,
   "providers":[{"cloud":"aws","instance_type":"g5.xlarge","regions":["us-east-1"]}]}]}`

const listingImage = "vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f"

const listingPlatform = `{"templates":[{"id":"embed","title":"Embed","description":"d","image":"` + listingImage + `",
  "command":["true"],"sku":"a10g-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":[],"available":true}]}`

func listingEntry(id, title string) map[string]any {
	var m map[string]any
	raw := `{"id":"` + id + `","title":"` + title + `","description":"d","image":"` + listingImage + `","command":["true"],
	  "sku":"a10g-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":["sku"],"available":true}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		panic(err)
	}
	return m
}

// withListings gives the fixture an open marketplace and a publisher whose
// account may run jobs.
func withListings(t *testing.T, environment string) (*fixture, session, int64) {
	t.Helper()
	f := newFixture(t)
	cat, err := sku.Parse([]byte(listingCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	set, err := template.Parse([]byte(listingPlatform), cat, 86400)
	if err != nil {
		t.Fatal(err)
	}
	f.h.listings = listings.New(f.db, cat, 86400, set, environment, f.clock.now)
	s := f.activeUser("pub@example.com")
	acct := f.accountOf("pub@example.com")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET compute_enabled = true WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	return f, s, acct
}

func (s session) req(method, path string, body any) req {
	return req{method: method, path: path, body: body, cookie: s.cookie, csrf: s.csrf}
}

func decodeListing(t *testing.T, raw []byte) listingView {
	t.Helper()
	var v listingView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return v
}

func TestListingsAreNotImplementedWithoutAMarketplace(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("pub@example.com")
	rec := f.do(s.req(http.MethodGet, "/listings", nil))
	wantErr(t, rec, http.StatusNotImplemented, "not_implemented", "not_implemented")
}

func TestSubmitEditAndUnlistAListing(t *testing.T) {
	f, s, acct := withListings(t, "staging")

	rec := f.do(s.req(http.MethodGet, "/listings", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"data":[]`) || !strings.Contains(rec.Body.String(), `"open":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	rec = f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-embed", "Acme")}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	l := decodeListing(t, rec.Body.Bytes())
	if l.Object != "compute.listing" || l.TemplateID != "acme-embed" || l.ReviewState != "pending" || !l.Listed || l.Public ||
		l.Template.Publisher != template.PublisherOfRecord || l.ReviewedAt != nil {
		t.Fatalf("%+v", l)
	}

	rec = f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-embed", "Again")}))
	wantErr(t, rec, http.StatusConflict, "conflict", "listing_exists")

	rec = f.do(s.req(http.MethodPatch, "/listings/"+l.ID, map[string]any{"template": listingEntry("acme-embed", "Acme v2")}))
	if rec.Code != 200 || decodeListing(t, rec.Body.Bytes()).Template.Title != "Acme v2" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = f.do(s.req(http.MethodPatch, "/listings/"+l.ID, map[string]any{"template": listingEntry("acme-other", "T")}))
	wantErr(t, rec, http.StatusBadRequest, "invalid_request", "listing_id_fixed")

	rec = f.do(s.req(http.MethodPost, "/listings/"+l.ID+"/unlist", nil))
	if rec.Code != 200 || decodeListing(t, rec.Body.Bytes()).Listed {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = f.do(s.req(http.MethodPost, "/listings/lst_01ARZ3NDEKTSV4RRFFQ69G5FAV/unlist", nil))
	wantErr(t, rec, http.StatusNotFound, "not_found", "not_found")

	if got := f.auditActions(acct); strings.Join(got, ",") != "signup,verify,listing_submitted,listing_updated,listing_unlisted" {
		t.Fatalf("audit %v", got)
	}
}

func TestListingSubmitIsValidatedBeforeItCounts(t *testing.T) {
	f, s, _ := withListings(t, "staging")
	cases := []struct {
		name string
		body any
		code string
	}{
		{"no template", map[string]any{}, "invalid_listing"},
		{"not an object", map[string]any{"template": "embed"}, "invalid_listing"},
		{"platform id", map[string]any{"template": listingEntry("embed", "T")}, "listing_id_taken"},
		{"unknown sku", map[string]any{"template": func() map[string]any { m := listingEntry("x", "T"); m["sku"] = "h100"; return m }()}, "invalid_listing"},
	}
	for _, tc := range cases {
		rec := f.do(s.req(http.MethodPost, "/listings", tc.body))
		if e := wantErr(t, rec, http.StatusBadRequest, "invalid_request", tc.code); e.Error.Param == "" {
			t.Errorf("%s: no param: %s", tc.name, rec.Body)
		}
	}
	// 20 writes an hour; the invalid ones above did not count.
	for i := 0; i < 20; i++ {
		rec := f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-"+string(rune('a'+i)), "T")}))
		if rec.Code != http.StatusCreated {
			t.Fatalf("submit %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	rec := f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-z", "T")}))
	wantErr(t, rec, http.StatusTooManyRequests, "rate_limited", "rate_limited")
}

func TestListingNeedsAnAccountThatCanRunJobs(t *testing.T) {
	f, s, acct := withListings(t, "staging")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET compute_enabled = false WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	rec := f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-embed", "T")}))
	wantErr(t, rec, http.StatusForbidden, "permission_error", "compute_not_enabled")
}

func TestListingsAreScopedToTheAccountAndSayWhenTheMarketIsClosed(t *testing.T) {
	f, s, _ := withListings(t, "production")
	rec := f.do(s.req(http.MethodPost, "/listings", map[string]any{"template": listingEntry("acme-embed", "T")}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	l := decodeListing(t, rec.Body.Bytes())
	rec = f.do(s.req(http.MethodGet, "/listings", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"open":false`) || !strings.Contains(rec.Body.String(), l.ID) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	other := f.activeUser("other@example.com")
	rec = f.do(other.req(http.MethodGet, "/listings", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("other account sees it: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(other.req(http.MethodPatch, "/listings/"+l.ID, map[string]any{"template": listingEntry("acme-embed", "Stolen")}))
	wantErr(t, rec, http.StatusNotFound, "not_found", "not_found")
	rec = f.do(other.req(http.MethodPost, "/listings/"+l.ID+"/unlist", nil))
	wantErr(t, rec, http.StatusNotFound, "not_found", "not_found")
}
