package quota

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// ContractMaxRuntimeSeconds is the most max_runtime_seconds the API contract
// (openapi.json) will ever accept. A deployment's ceiling may be lower, never
// higher.
const ContractMaxRuntimeSeconds int64 = 24 * 60 * 60

// Platform is what the whole deployment may have active at once, across every
// owner, and how long any one job may run. It lives apart from the per-owner
// limits in quotas.json because it answers a different question: not how much
// of us one account may take, but how much GPU spend the operator has agreed
// to have in flight at all (ADR-0010, "Start small"). The reaper's maximum
// age in deploy/aws/modules/gpu-workers is derived from MaxRuntimeSeconds in
// the same file, so the two cannot drift.
type Platform struct {
	Note              string `json:"note,omitempty"`
	MaxActiveJobs     int    `json:"max_active_jobs"`
	MaxGPUs           int    `json:"max_gpus"`
	MaxRuntimeSeconds int64  `json:"max_runtime_seconds"`
}

const (
	LimitPlatformActiveJobs = "platform_max_active_jobs"
	LimitPlatformGPUs       = "platform_max_gpus"
)

func LoadPlatform(path string) (*Platform, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePlatform(raw)
}

// ParsePlatform requires every field. A platform file that leaves a limit out
// would otherwise mean zero, which for max_active_jobs is "nothing runs" and
// for max_runtime_seconds is nonsense; both are better as a boot failure.
func ParsePlatform(raw []byte) (*Platform, error) {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(raw, &present); err != nil {
		return nil, fmt.Errorf("platform config: %w", err)
	}
	for _, k := range []string{"max_active_jobs", "max_gpus", "max_runtime_seconds"} {
		if _, ok := present[k]; !ok {
			return nil, fmt.Errorf("platform config: %s is required", k)
		}
	}
	var p Platform
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("platform config: %w", err)
	}
	if p.MaxActiveJobs < 0 || p.MaxGPUs < 0 {
		return nil, errors.New("platform config: limits must not be negative")
	}
	if p.MaxRuntimeSeconds <= 0 || p.MaxRuntimeSeconds > ContractMaxRuntimeSeconds {
		return nil, fmt.Errorf("platform config: max_runtime_seconds %d is outside (0, %d]", p.MaxRuntimeSeconds, ContractMaxRuntimeSeconds)
	}
	return &p, nil
}

// EvaluatePlatform is the platform-wide half of the decision: usage summed
// over every owner, plus one new queued job on item. GPUs are counted across
// tiers, because the cap is on spend in flight, not on any one upstream.
func EvaluatePlatform(p *Platform, usage Usage, item sku.SKU) []Violation {
	var vs []Violation
	gpus := 0
	for _, n := range usage.GPUs {
		gpus += n
	}
	if usage.ActiveJobs+1 > p.MaxActiveJobs {
		vs = append(vs, Violation{Limit: LimitPlatformActiveJobs, Max: p.MaxActiveJobs, Used: usage.ActiveJobs, Requested: 1,
			Headroom: max(0, p.MaxActiveJobs-usage.ActiveJobs)})
	}
	if gpus+item.GPUCount > p.MaxGPUs {
		vs = append(vs, Violation{Limit: LimitPlatformGPUs, Max: p.MaxGPUs, Used: gpus, Requested: item.GPUCount,
			Headroom: max(0, p.MaxGPUs-gpus)})
	}
	return vs
}
