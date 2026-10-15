package sku

import (
	"errors"
	"os"
	"testing"
)

func TestShippedCatalogueParses(t *testing.T) {
	raw, err := os.ReadFile("../../config/skus.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SKUs) == 0 {
		t.Fatal("catalogue is empty")
	}
	if c.Note == "" {
		t.Fatal("the illustrative-prices note was removed; that is a pricing release, not a cleanup")
	}
}

func TestEstimateQuotaMatchesConsoleUnit(t *testing.T) {
	s := SKU{ID: "x", PriceUSDPerHour: "1.2000"}
	// One hour at $1.20 is 1.2 * 500000 quota.
	got, err := s.EstimateQuota(3600)
	if err != nil {
		t.Fatal(err)
	}
	if got != 600_000 {
		t.Fatalf("got %d, want 600000", got)
	}
	// One second at $1.20/h is $0.000333…, i.e. 166.67 quota → 167.
	got, _ = s.EstimateQuota(1)
	if got != 167 {
		t.Fatalf("got %d, want 167 (half-up)", got)
	}
	usd, _ := s.CostUSD(1830)
	if usd != "0.6100" {
		t.Fatalf("got %q, want 0.6100", usd)
	}
}

func TestParseRejectsBadCatalogues(t *testing.T) {
	cases := map[string]string{
		"duplicate": `{"skus":[{"id":"a","price_usd_per_hour":"1"},{"id":"a","price_usd_per_hour":"1"}]}`,
		"no id":     `{"skus":[{"price_usd_per_hour":"1"}]}`,
		"bad price": `{"skus":[{"id":"a","price_usd_per_hour":"free"}]}`,
		"negative":  `{"skus":[{"id":"a","price_usd_per_hour":"-1"}]}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPlacementValidation(t *testing.T) {
	bad := map[string]string{
		"available without tier":      `{"skus":[{"id":"a","price_usd_per_hour":"1","available":true,"accelerator":"A10G","providers":[{"cloud":"aws","instance_type":"g5.xlarge"}]}]}`,
		"available without providers": `{"skus":[{"id":"a","price_usd_per_hour":"1","available":true,"tier":"hyperscaler","accelerator":"A10G"}]}`,
		"unknown tier":                `{"skus":[{"id":"a","price_usd_per_hour":"1","tier":"friendly"}]}`,
		"unknown cloud":               `{"skus":[{"id":"a","price_usd_per_hour":"1","providers":[{"cloud":"openai","instance_type":"x"}]}]}`,
		"duplicate cloud":             `{"skus":[{"id":"a","price_usd_per_hour":"1","providers":[{"cloud":"aws","instance_type":"x"},{"cloud":"aws","instance_type":"y"}]}]}`,
		"vm without instance type":    `{"skus":[{"id":"a","price_usd_per_hour":"1","providers":[{"cloud":"lambda"}]}]}`,
		"cluster without context":     `{"skus":[{"id":"a","price_usd_per_hour":"1","providers":[{"cloud":"kubernetes"}]}]}`,
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	// Unavailable SKUs may be partial: the L4 entry was one before it had providers.
	if _, err := Parse([]byte(`{"skus":[{"id":"a","price_usd_per_hour":"1","available":false}]}`)); err != nil {
		t.Fatalf("partial unavailable sku rejected: %v", err)
	}
}

func TestTopologyValidation(t *testing.T) {
	const providers = `"providers":[{"cloud":"aws","instance_type":"p4d.24xlarge","regions":["us-east-1"]}]`
	bad := map[string]string{
		"unknown interconnect":                     `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":8,"interconnect":"telepathy"}]}`,
		"unknown network":                          `{"skus":[{"id":"a","price_usd_per_hour":"1","network":"carrier-pigeon"}]}`,
		"interconnect on single gpu":               `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":1,"interconnect":"nvlink"}]}`,
		"available multi-gpu without interconnect": `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":8,"available":true,"tier":"hyperscaler","accelerator":"A100-80GB",` + providers + `}]}`,
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	good := map[string]string{
		"single gpu says nothing":              `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":1,"available":true,"tier":"hyperscaler","accelerator":"A10G","providers":[{"cloud":"aws","instance_type":"g5.xlarge"}]}]}`,
		"available multi-gpu nvlink":           `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":8,"interconnect":"nvlink","network":"infiniband","available":true,"tier":"hyperscaler","accelerator":"A100-80GB",` + providers + `}]}`,
		"unavailable multi-gpu may be partial": `{"skus":[{"id":"a","price_usd_per_hour":"1","gpu_count":8,"available":false}]}`,
	}
	for name, raw := range good {
		if _, err := Parse([]byte(raw)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A multi-GPU SKU may not be sold without saying how its GPUs are wired: on
// the same accelerator and the same price line, PCIe and NVSwitch differ by an
// order of magnitude.
func TestShippedCatalogueDeclaresInterconnectForMultiGPU(t *testing.T) {
	c, err := Load("../../config/skus.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range c.SKUs {
		if s.Available && s.GPUCount > 1 && s.Interconnect == "" {
			t.Errorf("sku %q sells %d GPUs without an interconnect", s.ID, s.GPUCount)
		}
	}
}

func TestRegionsDeriveFromProviders(t *testing.T) {
	c, err := Parse([]byte(`{"skus":[{"id":"a","price_usd_per_hour":"1","providers":[
		{"cloud":"aws","instance_type":"g5.xlarge","regions":["us-east-1","us-west-2"]},
		{"cloud":"gcp","instance_type":"g2-standard-4","regions":["us-central1","us-east-1"]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := c.SKUs[0].Regions
	want := []string{"us-central1", "us-east-1", "us-west-2"}
	if len(got) != len(want) {
		t.Fatalf("regions %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("regions %v, want %v", got, want)
		}
	}
}

func TestCandidatesRespectOrderAndRefuseUnknown(t *testing.T) {
	s := SKU{Providers: []Provider{{Cloud: "aws", InstanceType: "x"}, {Cloud: "lambda", InstanceType: "y"}, {Cloud: "runpod", InstanceType: "z"}}}
	all, _ := s.Candidates(nil)
	if len(all) != 3 {
		t.Fatalf("nil filter should return all, got %d", len(all))
	}
	// Filter order does not matter; catalogue order is preference order.
	got, err := s.Candidates([]string{"runpod", "aws"})
	if err != nil || len(got) != 2 || got[0].Cloud != "aws" || got[1].Cloud != "runpod" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := s.Candidates([]string{"aws", "azure"}); !errors.Is(err, ErrProviderNotOffered) {
		t.Fatalf("expected ErrProviderNotOffered, got %v", err)
	}
}

func TestShippedCatalogueHasPlacementForAvailableSKUs(t *testing.T) {
	raw, _ := os.ReadFile("../../config/skus.json")
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Clouds()["aws"]; !ok {
		t.Fatal("first upstream is AWS (ADR-0006); the shipped catalogue must reference it")
	}
	// ADR-0013: every listed SKU may be chosen; which of them launch is the
	// deployment's UPSTREAMS. Each must still say where it could run.
	for _, s := range c.SKUs {
		if s.Available && (s.Tier == "" || len(s.Providers) == 0) {
			t.Errorf("%s: a choosable SKU names its tier and providers", s.ID)
		}
	}
}
