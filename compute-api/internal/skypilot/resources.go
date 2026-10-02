// Package skypilot translates catalogue placement into what SkyPilot's API
// server accepts. It is the first piece of the backend (ADR-0003): pure
// translation, no network, so the shape a job spec becomes is testable before
// anything can be launched.
package skypilot

import (
	"strconv"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// Resources is the `resources` block of a SkyPilot task. JSON is valid YAML,
// so the marshalled form is exactly what `sky launch` and the API server read.
type Resources struct {
	Cloud        string      `json:"cloud,omitempty"`
	Region       string      `json:"region,omitempty"`
	InstanceType string      `json:"instance_type,omitempty"`
	Accelerators string      `json:"accelerators,omitempty"`
	UseSpot      bool        `json:"use_spot,omitempty"`
	AnyOf        []Resources `json:"any_of,omitempty"`
}

// ResourcesFor emits one resources block for a SKU and its candidate
// providers. One candidate (one cloud, one region) is emitted flat; several
// go under any_of in the order given, which SkyPilot treats as preference
// order and fails over along on capacity.
func ResourcesFor(s sku.SKU, providers []sku.Provider) Resources {
	var candidates []Resources
	for _, p := range providers {
		regions := p.Regions
		if len(regions) == 0 {
			regions = []string{""}
		}
		for _, r := range regions {
			c := Resources{Cloud: p.Cloud, Region: r, InstanceType: p.InstanceType}
			if p.IsCluster() {
				// SkyPilot addresses a Kubernetes context or SSH pool through
				// the region slot of the resources block.
				c.Region = p.Context
			}
			candidates = append(candidates, c)
		}
	}
	shared := Resources{UseSpot: s.Interruptible}
	if s.Accelerator != "" {
		shared.Accelerators = s.Accelerator + ":" + strconv.Itoa(max(s.GPUCount, 1))
	}
	if len(candidates) == 1 {
		c := candidates[0]
		c.Accelerators, c.UseSpot = shared.Accelerators, shared.UseSpot
		return c
	}
	shared.AnyOf = candidates
	return shared
}
