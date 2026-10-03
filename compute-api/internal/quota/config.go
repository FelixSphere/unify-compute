// Package quota decides whether an owner may start another job
// (docs/spec/quotas.md). Limits ship with the binary in config/quotas.json,
// like the SKU catalogue, so raising a customer's limit is a reviewed diff.
// A request over any limit is refused whole; nothing is ever partially
// granted.
package quota

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// Limits is what an owner may have active at once. Active means queued,
// provisioning or running: a queued job already holds balance and will claim
// its GPUs when it starts, so it counts from the moment it is accepted.
type Limits struct {
	MaxActiveJobs int              `json:"max_active_jobs"`
	MaxQueuedJobs int              `json:"max_queued_jobs"`
	MaxGPUs       map[sku.Tier]int `json:"max_gpus"`
}

// Override replaces individual fields of the defaults for one owner. A field
// left out keeps the default, and so does a tier left out of max_gpus.
type Override struct {
	MaxActiveJobs *int             `json:"max_active_jobs,omitempty"`
	MaxQueuedJobs *int             `json:"max_queued_jobs,omitempty"`
	MaxGPUs       map[sku.Tier]int `json:"max_gpus,omitempty"`
	Reason        string           `json:"reason"`
}

type Config struct {
	Note     string              `json:"note,omitempty"`
	Defaults Limits              `json:"defaults"`
	Owners   map[string]Override `json:"owners"`

	resolved map[string]Limits
}

// ownerPattern is the owner string a verified key resolves to: under the
// console bridge the billing entity, the tenant when there is one and the
// user otherwise (console-billing-bridge.md, verify); with compute's own
// accounts, account:<id> (accounts.md).
var ownerPattern = regexp.MustCompile(`^(user|tenant|account):[1-9][0-9]*$`)

var allTiers = []sku.Tier{sku.TierHyperscaler, sku.TierNeocloud, sku.TierMarketplace}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse refuses anything it cannot apply exactly: unknown fields, unknown
// tiers, a default without every tier, negative limits, a malformed owner,
// or an override with no reason. A quota file that does not parse stops the
// process, as a bad catalogue does.
func Parse(raw []byte) (*Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("quota config: %w", err)
	}
	for _, t := range allTiers {
		if _, ok := c.Defaults.MaxGPUs[t]; !ok {
			return nil, fmt.Errorf("quota config: defaults.max_gpus has no %q entry", t)
		}
	}
	if err := c.Defaults.validate(); err != nil {
		return nil, fmt.Errorf("quota config: defaults: %w", err)
	}
	c.resolved = make(map[string]Limits, len(c.Owners))
	owners := make([]string, 0, len(c.Owners))
	for o := range c.Owners {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	for _, o := range owners {
		ov := c.Owners[o]
		if !ownerPattern.MatchString(o) {
			return nil, fmt.Errorf("quota config: owner %q is not user:<id>, tenant:<id> or account:<id>", o)
		}
		if ov.Reason == "" {
			return nil, fmt.Errorf("quota config: owner %q: override needs a reason", o)
		}
		l := c.Defaults.apply(ov)
		if err := l.validate(); err != nil {
			return nil, fmt.Errorf("quota config: owner %q: %w", o, err)
		}
		c.resolved[o] = l
	}
	return &c, nil
}

func (l Limits) apply(ov Override) Limits {
	out := Limits{MaxActiveJobs: l.MaxActiveJobs, MaxQueuedJobs: l.MaxQueuedJobs, MaxGPUs: map[sku.Tier]int{}}
	for t, n := range l.MaxGPUs {
		out.MaxGPUs[t] = n
	}
	if ov.MaxActiveJobs != nil {
		out.MaxActiveJobs = *ov.MaxActiveJobs
	}
	if ov.MaxQueuedJobs != nil {
		out.MaxQueuedJobs = *ov.MaxQueuedJobs
	}
	for t, n := range ov.MaxGPUs {
		out.MaxGPUs[t] = n
	}
	return out
}

func (l Limits) validate() error {
	if l.MaxActiveJobs < 0 || l.MaxQueuedJobs < 0 {
		return errors.New("limits must not be negative")
	}
	// Queued jobs are a subset of active ones, so a larger queue limit could
	// never bind; it is almost certainly a typo for something else.
	if l.MaxQueuedJobs > l.MaxActiveJobs {
		return fmt.Errorf("max_queued_jobs %d exceeds max_active_jobs %d", l.MaxQueuedJobs, l.MaxActiveJobs)
	}
	for t, n := range l.MaxGPUs {
		if !knownTier(t) {
			return fmt.Errorf("max_gpus: unknown tier %q", t)
		}
		if n < 0 {
			return fmt.Errorf("max_gpus[%s] must not be negative", t)
		}
	}
	return nil
}

func knownTier(t sku.Tier) bool {
	for _, k := range allTiers {
		if k == t {
			return true
		}
	}
	return false
}

// For returns owner's limits: its override applied to the defaults, or the
// defaults alone.
func (c *Config) For(owner string) Limits {
	if l, ok := c.resolved[owner]; ok {
		return l
	}
	return c.Defaults
}
