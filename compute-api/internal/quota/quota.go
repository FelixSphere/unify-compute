package quota

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Usage is an owner's active footprint, in the units the limits use.
type Usage struct {
	ActiveJobs int
	QueuedJobs int
	GPUs       map[sku.Tier]int
}

// Violation is one limit a submit would exceed. Headroom is what is left
// under the limit now; it is 0, never negative, when a lowered limit leaves
// the owner above it.
type Violation struct {
	Limit     string   `json:"limit"`
	Tier      sku.Tier `json:"tier,omitempty"`
	Max       int      `json:"max"`
	Used      int      `json:"used"`
	Requested int      `json:"requested"`
	Headroom  int      `json:"headroom"`
}

const (
	LimitActiveJobs = "max_active_jobs"
	LimitQueuedJobs = "max_queued_jobs"
	LimitGPUs       = "max_gpus"
)

// ExceededError refuses a submit. It lists every limit the submit would
// exceed, not just the first, so the customer fixes the request once.
type ExceededError struct {
	Owner      string
	SKU        string
	Violations []Violation
}

func (e *ExceededError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		name := v.Limit
		if v.Tier != "" {
			name = fmt.Sprintf("%s GPUs", v.Tier)
		} else if v.Limit == LimitActiveJobs {
			name = "active jobs"
		} else if v.Limit == LimitQueuedJobs {
			name = "queued jobs"
		}
		parts = append(parts, fmt.Sprintf("%s: %d of %d in use, this job needs %d, headroom %d", name, v.Used, v.Max, v.Requested, v.Headroom))
	}
	return fmt.Sprintf("sku %q would exceed this account's quota (%s). Nothing was submitted or held; wait for or cancel an active job, choose a smaller SKU, or ask support to raise the limit.",
		e.SKU, strings.Join(parts, "; "))
}

// ErrUnknownActiveSKU means the owner has an active job on a SKU the
// catalogue no longer lists, so its GPUs and tier cannot be counted. The
// check fails closed rather than guess; retire SKUs with available=false,
// never by deleting them while jobs may still run on them.
var ErrUnknownActiveSKU = errors.New("quota: active job on a sku the catalogue does not list")

// Evaluate is the whole decision: the owner's usage plus one new queued job
// on item, against limits. It returns nil or an *ExceededError.
func Evaluate(owner string, limits Limits, usage Usage, item sku.SKU) error {
	var vs []Violation
	check := func(name string, tier sku.Tier, limit, used, req int) {
		if used+req > limit {
			vs = append(vs, Violation{Limit: name, Tier: tier, Max: limit, Used: used, Requested: req, Headroom: max(0, limit-used)})
		}
	}
	check(LimitActiveJobs, "", limits.MaxActiveJobs, usage.ActiveJobs, 1)
	check(LimitQueuedJobs, "", limits.MaxQueuedJobs, usage.QueuedJobs, 1)
	check(LimitGPUs, item.Tier, limits.MaxGPUs[item.Tier], usage.GPUs[item.Tier], item.GPUCount)
	if len(vs) == 0 {
		return nil
	}
	return &ExceededError{Owner: owner, SKU: item.ID, Violations: vs}
}

// Tally converts the store's per-SKU counts into Usage with the catalogue.
func Tally(cat *sku.Catalogue, rows []store.SKUUsage) (Usage, error) {
	u := Usage{GPUs: map[sku.Tier]int{}}
	for _, r := range rows {
		item, err := cat.Get(r.SKU)
		if err != nil {
			return Usage{}, fmt.Errorf("%w: %q", ErrUnknownActiveSKU, r.SKU)
		}
		u.ActiveJobs += r.Active
		u.QueuedJobs += r.Queued
		u.GPUs[item.Tier] += r.Active * item.GPUCount
	}
	return u, nil
}

// UsageReader is the read the preflight needs; *store.Store satisfies it.
type UsageReader interface {
	ActiveUsage(ctx context.Context, owner string) ([]store.SKUUsage, error)
}

type Enforcer struct {
	Config    *Config
	Catalogue *sku.Catalogue
	Usage     UsageReader
}

// Check is the preflight the submit handler runs before it holds anything.
// It reads usage without a lock, so two concurrent submits can both pass
// it; Admit is the authoritative check that cannot race, and a submit that
// persists a job must go through it.
func (e *Enforcer) Check(ctx context.Context, owner string, item sku.SKU) error {
	rows, err := e.Usage.ActiveUsage(ctx, owner)
	if err != nil {
		return fmt.Errorf("quota: read usage: %w", err)
	}
	return e.decide(owner, item, rows)
}

func (e *Enforcer) decide(owner string, item sku.SKU, rows []store.SKUUsage) error {
	u, err := Tally(e.Catalogue, rows)
	if err != nil {
		return err
	}
	return Evaluate(owner, e.Config.For(owner), u, item)
}

// Admit returns a store.AdmitFunc for store.CreateAdmitted: it decides
// against the usage read under the owner's lock and, only if the submit is
// within quota, runs then (where the console hold goes). An over-quota
// submit therefore never reaches the console.
func (e *Enforcer) Admit(owner string, item sku.SKU, then func(context.Context) error) store.AdmitFunc {
	return func(ctx context.Context, rows []store.SKUUsage) error {
		if err := e.decide(owner, item, rows); err != nil {
			return err
		}
		if then == nil {
			return nil
		}
		return then(ctx)
	}
}
