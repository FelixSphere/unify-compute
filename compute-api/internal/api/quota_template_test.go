package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// The quota check runs after template expansion, on the SKU the template
// resolved to: a template is not a way around the account's limits, and a
// broken template submit is told what is wrong with it, not that the account
// is full.
func TestQuotaRunsAfterTemplateExpansion(t *testing.T) {
	var asked bool
	full := func(string) ([]store.SKUUsage, error) {
		asked = true
		return []store.SKUUsage{{SKU: "a10g-24gb-x1", Active: 2}}, nil
	}
	s := withQuota(t, newTemplateServer(t, "aws"), full)

	rec := submitAs(t, s, "user:1", `{"template":"embed","spec":{`+in+`}}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"quota_exceeded"`) {
		t.Fatalf("template on a full account: %d %s", rec.Code, rec.Body.String())
	}
	if !asked {
		t.Fatal("quota was not consulted for a template submit")
	}

	asked = false
	rec = submitAs(t, s, "user:1", `{"template":"embed","spec":{"image":"x",`+in+`}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "field_not_overridable") {
		t.Fatalf("template error on a full account: %d %s", rec.Code, rec.Body.String())
	}
	rec = submitAs(t, s, "user:1", `{"template":"nope"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown_template") {
		t.Fatalf("unknown template on a full account: %d %s", rec.Code, rec.Body.String())
	}
	if asked {
		t.Fatal("quota was consulted before the template was resolved")
	}
}
