package sku

import (
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
