// Package sku holds the compute price catalogue. Prices ship with the binary so
// a change is a reviewable diff, mirroring how the console keeps its model
// price baseline in code rather than in the database.
package sku

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
)

// QuotaPerUSD is the console's quota unit: new-api's QuotaPerUnit, 500000 per
// USD 1. It is the only place this constant lives in compute-api.
const QuotaPerUSD = 500_000

type SKU struct {
	ID              string   `json:"id"`
	Object          string   `json:"object"`
	GPU             string   `json:"gpu"`
	GPUCount        int      `json:"gpu_count"`
	GPUMemoryGB     int      `json:"gpu_memory_gb"`
	VCPUs           int      `json:"vcpus"`
	MemoryGB        int      `json:"memory_gb"`
	PriceUSDPerHour string   `json:"price_usd_per_hour"`
	Regions         []string `json:"regions"`
	Available       bool     `json:"available"`
}

type Catalogue struct {
	Note string `json:"note,omitempty"`
	SKUs []SKU  `json:"skus"`
}

var ErrUnknown = errors.New("unknown sku")

func Load(path string) (*Catalogue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Catalogue, error) {
	var c Catalogue
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("sku catalogue: %w", err)
	}
	seen := map[string]bool{}
	for i := range c.SKUs {
		s := &c.SKUs[i]
		if s.ID == "" {
			return nil, errors.New("sku catalogue: sku without id")
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("sku catalogue: duplicate sku %q", s.ID)
		}
		seen[s.ID] = true
		if _, err := parseUSD(s.PriceUSDPerHour); err != nil {
			return nil, fmt.Errorf("sku %q: %w", s.ID, err)
		}
		s.Object = "compute.sku"
	}
	sort.Slice(c.SKUs, func(i, j int) bool { return c.SKUs[i].ID < c.SKUs[j].ID })
	return &c, nil
}

func (c *Catalogue) Get(id string) (SKU, error) {
	for _, s := range c.SKUs {
		if s.ID == id {
			return s, nil
		}
	}
	return SKU{}, ErrUnknown
}

// EstimateQuota prices a run of the given length in the console's quota unit,
// rounded half-up. This is the number a hold is placed for.
func (s SKU) EstimateQuota(seconds int64) (int64, error) {
	hourly, err := parseUSD(s.PriceUSDPerHour)
	if err != nil {
		return 0, err
	}
	// hourly * seconds / 3600 * QuotaPerUSD, exact until the final rounding.
	amount := new(big.Rat).Mul(hourly, big.NewRat(seconds, 3600))
	amount.Mul(amount, big.NewRat(QuotaPerUSD, 1))
	return roundHalfUp(amount), nil
}

// CostUSD renders the price of a run as a 4-decimal USD string for API bodies.
func (s SKU) CostUSD(seconds int64) (string, error) {
	hourly, err := parseUSD(s.PriceUSDPerHour)
	if err != nil {
		return "", err
	}
	amount := new(big.Rat).Mul(hourly, big.NewRat(seconds, 3600))
	return amount.FloatString(4), nil
}

func parseUSD(s string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("invalid usd amount %q", s)
	}
	return r, nil
}

func roundHalfUp(r *big.Rat) int64 {
	num := new(big.Int).Mul(r.Num(), big.NewInt(2))
	num.Add(num, r.Denom())
	den := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	return new(big.Int).Quo(num, den).Int64()
}
