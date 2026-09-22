// Package upstream is the set of capacity sources this deployment may launch
// into. The catalogue says where a SKU could run; the registry says where this
// process holds credentials to send it (ADR-0006).
package upstream

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/skypilot"
)

type Registry struct {
	enabled map[string]bool
}

var ErrNoEnabledProvider = errors.New("no enabled upstream offers this sku")

// Parse reads a comma-separated list of SkyPilot cloud names (the UPSTREAMS
// environment variable). Empty is a valid registry that can place nothing.
func Parse(list string) (*Registry, error) {
	r := &Registry{enabled: map[string]bool{}}
	for _, c := range strings.Split(list, ",") {
		c = strings.TrimSpace(strings.ToLower(c))
		if c == "" {
			continue
		}
		if !sku.KnownClouds[c] {
			return nil, fmt.Errorf("UPSTREAMS: %q is not a SkyPilot cloud", c)
		}
		r.enabled[c] = true
	}
	return r, nil
}

func (r *Registry) Enabled(cloud string) bool { return r.enabled[cloud] }

func (r *Registry) Clouds() []string {
	out := make([]string, 0, len(r.enabled))
	for c := range r.enabled {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Place resolves where a job may run: the SKU's providers, narrowed to the
// customer's `only` list, narrowed to enabled upstreams, in catalogue order.
// sku.ErrProviderNotOffered means the customer asked for something the SKU
// never offers (their mistake); ErrNoEnabledProvider means the SKU is sound
// but this deployment cannot launch it (ours).
func (r *Registry) Place(s sku.SKU, only []string) (skypilot.Resources, []sku.Provider, error) {
	candidates, err := s.Candidates(only)
	if err != nil {
		return skypilot.Resources{}, nil, err
	}
	var live []sku.Provider
	for _, p := range candidates {
		if r.enabled[p.Cloud] {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return skypilot.Resources{}, nil, ErrNoEnabledProvider
	}
	return skypilot.ResourcesFor(s, live), live, nil
}
