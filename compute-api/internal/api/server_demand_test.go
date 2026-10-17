package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

type demandSpy struct{ got []string }

func (d *demandSpy) Record(_ context.Context, owner, skuID string) {
	d.got = append(d.got, owner+" "+skuID)
}

// A listed SKU none of whose providers this deployment has enabled is
// demand (ADR-0013): refused as gpu_not_provisioned and recorded, while a
// customer who names a disabled provider still gets upstream_unavailable
// (server_test.go, "provider offered but disabled").
func TestUnprovisionedSKUIsRecordedAsDemand(t *testing.T) {
	cat, err := sku.Parse([]byte(testCatalogue[:len(testCatalogue)-3] + `,
  {"id":"a100-80gb-x1-neo","price_usd_per_hour":"2.0000","available":true,"tier":"neocloud","accelerator":"A100-80GB","gpu_count":1,
   "providers":[{"cloud":"lambda","instance_type":"gpu_1x_a100_sxm4","regions":["us-east-1"]}]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := upstream.Parse("aws")
	if err != nil {
		t.Fatal(err)
	}
	s := New("test", cat, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	spy := &demandSpy{}
	s.Demand = spy
	rec := do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"sku":"a100-80gb-x1-neo","spec":{"max_runtime_seconds":10}}`)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"gpu_not_provisioned"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// The dark build knows no owner; the record still names the SKU.
	if len(spy.got) != 1 || !strings.HasSuffix(spy.got[0], " a100-80gb-x1-neo") {
		t.Fatalf("demand recorded: %v", spy.got)
	}
	// Without a recorder the refusal is the same.
	s.Demand = nil
	rec = do(t, s, http.MethodPost, "/compute/v1/jobs", "sk-abcdefghij", `{"sku":"a100-80gb-x1-neo","spec":{"max_runtime_seconds":10}}`)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"gpu_not_provisioned"`) {
		t.Fatalf("without recorder: %d %s", rec.Code, rec.Body.String())
	}
}
