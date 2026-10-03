package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

func shippedPlatform(t *testing.T) *Platform {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p, err := LoadPlatform(filepath.Join(filepath.Dir(file), "..", "..", "config", "platform.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// ADR-0010, "Start small" (2026-10-03): one GPU job at a time on the whole
// platform, four hours at most. The reaper and the overshoot bound in the ADR
// are computed from these numbers, so a change here is a change there.
func TestShippedPlatformLimitsAreStartSmall(t *testing.T) {
	p := shippedPlatform(t)
	if p.MaxActiveJobs != 1 || p.MaxGPUs != 1 || p.MaxRuntimeSeconds != 4*3600 {
		t.Fatalf("shipped platform limits changed without updating ADR-0010 and docs/spec/quotas.md: %+v", p)
	}
}

func TestParsePlatformRefusesWhatItCannotApplyExactly(t *testing.T) {
	cases := map[string]string{
		"not json":            `nope`,
		"missing active jobs": `{"max_gpus":1,"max_runtime_seconds":3600}`,
		"missing gpus":        `{"max_active_jobs":1,"max_runtime_seconds":3600}`,
		"missing runtime":     `{"max_active_jobs":1,"max_gpus":1}`,
		"unknown field":       `{"max_active_jobs":1,"max_gpus":1,"max_runtime_seconds":3600,"max_jobs":2}`,
		"negative jobs":       `{"max_active_jobs":-1,"max_gpus":1,"max_runtime_seconds":3600}`,
		"negative gpus":       `{"max_active_jobs":1,"max_gpus":-1,"max_runtime_seconds":3600}`,
		"zero runtime":        `{"max_active_jobs":1,"max_gpus":1,"max_runtime_seconds":0}`,
		"runtime over 24 h":   `{"max_active_jobs":1,"max_gpus":1,"max_runtime_seconds":86401}`,
	}
	for name, raw := range cases {
		if _, err := ParsePlatform([]byte(raw)); err == nil {
			t.Errorf("%s: parsed, want refusal", name)
		}
	}
	// Zero jobs is a legal off switch: nothing new runs anywhere.
	if p, err := ParsePlatform([]byte(`{"max_active_jobs":0,"max_gpus":0,"max_runtime_seconds":86400}`)); err != nil || p.MaxActiveJobs != 0 {
		t.Fatalf("off switch: %+v %v", p, err)
	}
}

func TestEvaluatePlatformCountsEveryTierAndRefusesWhole(t *testing.T) {
	p := &Platform{MaxActiveJobs: 2, MaxGPUs: 2, MaxRuntimeSeconds: 3600}
	if vs := EvaluatePlatform(p, Usage{ActiveJobs: 1, GPUs: map[sku.Tier]int{}}, item(t, "a10g-x1")); len(vs) != 0 {
		t.Fatalf("within limits: %+v", vs)
	}
	// One hyperscaler and one neocloud GPU in use fill a 2-GPU platform.
	used := Usage{ActiveJobs: 1, GPUs: map[sku.Tier]int{"hyperscaler": 1, "neocloud": 1}}
	vs := EvaluatePlatform(p, used, item(t, "a10g-x1"))
	if len(vs) != 1 || vs[0].Limit != LimitPlatformGPUs || vs[0].Used != 2 || vs[0].Headroom != 0 || vs[0].Tier != "" {
		t.Fatalf("gpus across tiers: %+v", vs)
	}
	// A 4-GPU job against an empty 2-GPU platform is refused, not run on 2.
	vs = EvaluatePlatform(p, Usage{GPUs: map[sku.Tier]int{}}, item(t, "a10g-x4"))
	if len(vs) != 1 || vs[0].Requested != 4 || vs[0].Headroom != 2 {
		t.Fatalf("whole refusal: %+v", vs)
	}
	// A lowered limit never reports negative headroom.
	vs = EvaluatePlatform(&Platform{MaxActiveJobs: 1, MaxGPUs: 1, MaxRuntimeSeconds: 1}, Usage{ActiveJobs: 3, GPUs: map[sku.Tier]int{"hyperscaler": 3}}, item(t, "a10g-x1"))
	if len(vs) != 2 || vs[0].Headroom != 0 || vs[1].Headroom != 0 {
		t.Fatalf("lowered: %+v", vs)
	}
}

func TestAdmitPlatformReportsOwnerAndPlatformTogetherAndHoldsNothing(t *testing.T) {
	e := &Enforcer{Config: config(t), Catalogue: catalogue(t), Platform: &Platform{MaxActiveJobs: 1, MaxGPUs: 1, MaxRuntimeSeconds: 3600}}
	var holds int
	hold := func(context.Context) error { holds++; return nil }
	a10g := item(t, "a10g-x1")

	// Another owner's job fills the platform; this owner has nothing.
	other := []store.SKUUsage{{SKU: "a10g-x1", Active: 1}}
	err := e.AdmitPlatform("user:1", a10g, hold)(context.Background(), nil, other)
	var ex *ExceededError
	if !errors.As(err, &ex) || holds != 0 {
		t.Fatalf("err=%v holds=%d", err, holds)
	}
	if len(ex.Violations) != 2 || ex.Violations[0].Limit != LimitPlatformActiveJobs || ex.Violations[1].Limit != LimitPlatformGPUs {
		t.Fatalf("violations %+v", ex.Violations)
	}
	if !strings.Contains(ex.Error(), "platform-wide active jobs: 1 of 1 in use") || !strings.Contains(ex.Error(), "platform's current capacity") {
		t.Fatalf("message %q", ex.Error())
	}

	// Over the owner's own limit and the platform's: both in one refusal.
	mine := []store.SKUUsage{{SKU: "a10g-x1", Active: 2}}
	err = e.AdmitPlatform("user:1", a10g, hold)(context.Background(), mine, mine)
	if !errors.As(err, &ex) || len(ex.Violations) != 4 || holds != 0 {
		t.Fatalf("err=%v holds=%d", err, holds)
	}

	if err := e.AdmitPlatform("user:1", a10g, hold)(context.Background(), nil, nil); err != nil || holds != 1 {
		t.Fatalf("empty platform: err=%v holds=%d", err, holds)
	}

	// An unknown SKU anywhere on the platform fails closed, as for an owner.
	ghost := []store.SKUUsage{{SKU: "retired", Active: 1}}
	if err := e.AdmitPlatform("user:1", a10g, hold)(context.Background(), nil, ghost); !errors.Is(err, ErrUnknownActiveSKU) {
		t.Fatalf("unknown sku: %v", err)
	}
}

// Against Postgres: many owners, one platform slot, all submitting at once.
// Exactly one is admitted, every refusal saw the winner's job, and a
// per-owner-only submit running beside them does not deadlock.
func TestPlatformAdmitUnderConcurrencyAgainstPostgres(t *testing.T) {
	url := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	s := openSchema(t, url)
	ctx := context.Background()
	e := &Enforcer{Config: config(t), Catalogue: catalogue(t), Usage: s, Platform: &Platform{MaxActiveJobs: 1, MaxGPUs: 1, MaxRuntimeSeconds: 3600}}
	a10g := item(t, "a10g-x1")

	const n = 12
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner := fmt.Sprintf("user:%d", i+1)
			nj := store.NewJob{Owner: owner, SKU: a10g.ID, Spec: []byte(`{"max_runtime_seconds":60}`)}
			_, created, err := s.CreateAdmittedPlatform(ctx, nj, nil, e.AdmitPlatform(owner, a10g, nil))
			results <- result{created, err}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		nj := store.NewJob{Owner: "user:1", SKU: a10g.ID, Spec: []byte(`{"max_runtime_seconds":60}`)}
		if _, _, err := s.CreateAdmitted(ctx, nj, nil, func(context.Context, []store.SKUUsage) error { return errors.New("refused") }); err == nil {
			t.Error("per-owner admit that refuses must not insert")
		}
	}()
	wg.Wait()
	close(results)

	var created int
	for r := range results {
		var ex *ExceededError
		switch {
		case r.err == nil && r.created:
			created++
		case errors.As(r.err, &ex):
			for _, v := range ex.Violations {
				if v.Limit == LimitPlatformActiveJobs && (v.Used != 1 || v.Headroom != 0) {
					t.Errorf("refusal saw %+v, want the winner's job in use", v)
				}
			}
		default:
			t.Errorf("unexpected: %+v", r)
		}
	}
	if created != 1 {
		t.Fatalf("%d submits admitted into one platform slot", created)
	}
}
