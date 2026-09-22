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

// Tier is the trust level of every provider behind a SKU (ADR-0006). The
// customer chooses it by choosing the SKU; placement never crosses it.
type Tier string

const (
	TierHyperscaler Tier = "hyperscaler"
	TierNeocloud    Tier = "neocloud"
	TierMarketplace Tier = "marketplace"
)

var tiers = map[Tier]bool{TierHyperscaler: true, TierNeocloud: true, TierMarketplace: true}

// KnownClouds are the names SkyPilot accepts in a task's `resources.cloud`.
// A catalogue entry naming anything else would fail at launch, so it fails
// at boot instead.
var KnownClouds = map[string]bool{
	"aws": true, "gcp": true, "azure": true, "oci": true, "ibm": true, "scp": true, "nebius": true,
	"lambda": true, "runpod": true, "fluidstack": true, "paperspace": true, "cudo": true, "vast": true,
	"shadeform": true, "seeweb": true, "do": true, "hyperbolic": true, "primeintellect": true,
	"kubernetes": true, "ssh": true, "vsphere": true,
}

// clusterClouds address capacity by context (a kubeconfig context or an SSH
// node pool), not by instance type.
var clusterClouds = map[string]bool{"kubernetes": true, "ssh": true}

// Provider is one place a SKU can run. Order within a SKU is preference order.
type Provider struct {
	Cloud        string   `json:"cloud"`
	InstanceType string   `json:"instance_type,omitempty"`
	Context      string   `json:"context,omitempty"`
	Regions      []string `json:"regions,omitempty"`
}

// IsCluster reports whether the provider is addressed by context rather than
// instance type.
func (p Provider) IsCluster() bool { return clusterClouds[p.Cloud] }

type SKU struct {
	ID              string     `json:"id"`
	Object          string     `json:"object"`
	GPU             string     `json:"gpu"`
	Accelerator     string     `json:"accelerator"`
	GPUCount        int        `json:"gpu_count"`
	GPUMemoryGB     int        `json:"gpu_memory_gb"`
	VCPUs           int        `json:"vcpus"`
	MemoryGB        int        `json:"memory_gb"`
	PriceUSDPerHour string     `json:"price_usd_per_hour"`
	Tier            Tier       `json:"tier"`
	Interruptible   bool       `json:"interruptible"`
	Regions         []string   `json:"regions"`
	Providers       []Provider `json:"providers"`
	Available       bool       `json:"available"`
}

type Catalogue struct {
	Note string `json:"note,omitempty"`
	SKUs []SKU  `json:"skus"`
}

var (
	ErrUnknown            = errors.New("unknown sku")
	ErrProviderNotOffered = errors.New("provider not offered for sku")
)

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
		if err := s.validatePlacement(); err != nil {
			return nil, fmt.Errorf("sku %q: %w", s.ID, err)
		}
		s.Object = "compute.sku"
	}
	sort.Slice(c.SKUs, func(i, j int) bool { return c.SKUs[i].ID < c.SKUs[j].ID })
	return &c, nil
}

func (s *SKU) validatePlacement() error {
	if s.Tier != "" && !tiers[s.Tier] {
		return fmt.Errorf("unknown tier %q", s.Tier)
	}
	if s.Available {
		switch {
		case s.Tier == "":
			return errors.New("available sku has no tier")
		case s.Accelerator == "":
			return errors.New("available sku has no accelerator")
		case len(s.Providers) == 0:
			return errors.New("available sku has no providers")
		}
	}
	clouds := map[string]bool{}
	regions := map[string]bool{}
	for _, p := range s.Providers {
		if !KnownClouds[p.Cloud] {
			return fmt.Errorf("provider cloud %q is not a SkyPilot cloud", p.Cloud)
		}
		if clouds[p.Cloud] {
			return fmt.Errorf("provider cloud %q listed twice", p.Cloud)
		}
		clouds[p.Cloud] = true
		if p.IsCluster() && p.Context == "" {
			return fmt.Errorf("provider %q needs a context", p.Cloud)
		}
		if !p.IsCluster() && p.InstanceType == "" {
			return fmt.Errorf("provider %q needs an instance_type", p.Cloud)
		}
		for _, r := range p.Regions {
			regions[r] = true
		}
	}
	if len(s.Regions) == 0 {
		for r := range regions {
			s.Regions = append(s.Regions, r)
		}
		sort.Strings(s.Regions)
	}
	return nil
}

func (c *Catalogue) Get(id string) (SKU, error) {
	for _, s := range c.SKUs {
		if s.ID == id {
			return s, nil
		}
	}
	return SKU{}, ErrUnknown
}

// Clouds lists every cloud any SKU in the catalogue can run on, with the SKUs
// that name it, sorted by cloud.
func (c *Catalogue) Clouds() map[string][]string {
	out := map[string][]string{}
	for _, s := range c.SKUs {
		for _, p := range s.Providers {
			out[p.Cloud] = append(out[p.Cloud], s.ID)
		}
	}
	return out
}

// Candidates returns the SKU's providers restricted to `only`, in catalogue
// order. A nil or empty `only` means all of them. Naming a cloud the SKU does
// not offer is ErrProviderNotOffered wrapped with the offending name.
func (s SKU) Candidates(only []string) ([]Provider, error) {
	if len(only) == 0 {
		return s.Providers, nil
	}
	want := map[string]bool{}
	for _, c := range only {
		want[c] = true
	}
	var out []Provider
	for _, p := range s.Providers {
		if want[p.Cloud] {
			out = append(out, p)
			delete(want, p.Cloud)
		}
	}
	if len(want) > 0 {
		var missing []string
		for c := range want {
			missing = append(missing, c)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: %v", ErrProviderNotOffered, missing)
	}
	return out, nil
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
