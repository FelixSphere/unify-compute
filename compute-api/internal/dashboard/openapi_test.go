package dashboard

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
)

// TestRoutesAndOpenAPIAgree holds the registered routes against
// docs/spec/dashboard-openapi.json in both directions, as the job API's
// test does for its own document.
func TestRoutesAndOpenAPIAgree(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/spec/dashboard-openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	var documented []string
	for path, item := range doc.Paths {
		for method := range item {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	h, err := New(Config{Accounts: &accounts.Service{}, Mailer: &mailer.Mailer{}, AppOrigin: testOrigin, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	registered := h.Patterns()
	slices.Sort(documented)
	slices.Sort(registered)
	if !slices.Equal(documented, registered) {
		t.Errorf("routes and dashboard-openapi.json differ\nregistered %v\ndocumented %v", registered, documented)
	}
}
