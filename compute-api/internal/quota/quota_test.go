package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const testCatalogue = `{"skus":[
  {"id":"a10g-x1","price_usd_per_hour":"1.2000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":1,
   "providers":[{"cloud":"aws","instance_type":"g5.xlarge"}]},
  {"id":"a10g-x4","price_usd_per_hour":"4.8000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":4,
   "providers":[{"cloud":"aws","instance_type":"g5.12xlarge"}]},
  {"id":"a100-neo","price_usd_per_hour":"1.8000","available":true,"tier":"neocloud","accelerator":"A100","gpu_count":1,
   "providers":[{"cloud":"lambda","instance_type":"gpu_1x_a100"}]},
  {"id":"rtx-vast","price_usd_per_hour":"0.3000","available":true,"tier":"marketplace","accelerator":"RTX4090","gpu_count":1,
   "providers":[{"cloud":"vast","instance_type":"1x-RTX4090"}]}
]}`

const testConfig = `{
  "defaults": {"max_active_jobs": 2, "max_queued_jobs": 2, "max_gpus": {"hyperscaler": 2, "neocloud": 2, "marketplace": 0}},
  "owners": {
    "tenant:7": {"max_active_jobs": 8, "max_gpus": {"hyperscaler": 8, "marketplace": 2}, "reason": "design partner, reviewed 2026-10-01"}
  }
}`

func catalogue(t *testing.T) *sku.Catalogue {
	t.Helper()
	c, err := sku.Parse([]byte(testCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func config(t *testing.T) *Config {
	t.Helper()
	c, err := Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func item(t *testing.T, id string) sku.SKU {
	t.Helper()
	s, err := catalogue(t).Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestShippedConfigParses(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	c, err := Load(filepath.Join(filepath.Dir(file), "..", "..", "config", "quotas.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := c.Defaults
	if d.MaxActiveJobs != 2 || d.MaxQueuedJobs != 2 ||
		d.MaxGPUs[sku.TierHyperscaler] != 2 || d.MaxGPUs[sku.TierNeocloud] != 2 || d.MaxGPUs[sku.TierMarketplace] != 0 {
		t.Fatalf("shipped defaults changed without updating docs/spec/quotas.md: %+v", d)
	}
}

func TestParseRefusesWhatItCannotApplyExactly(t *testing.T) {
	cases := map[string]string{
		"unknown field":         `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"max_jobs":3}`,
		"missing tier":          `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2}}}`,
		"unknown tier":          `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0,"onprem":1}}}`,
		"negative":              `{"defaults":{"max_active_jobs":-1,"max_queued_jobs":0,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}}}`,
		"queue over active":     `{"defaults":{"max_active_jobs":2,"max_queued_jobs":3,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}}}`,
		"bad owner":             `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"owners":{"alice":{"max_active_jobs":3,"reason":"x"}}}`,
		"override no reason":    `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"owners":{"user:1":{"max_active_jobs":3}}}`,
		"override negative":     `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"owners":{"user:1":{"max_gpus":{"neocloud":-2},"reason":"x"}}}`,
		"override breaks queue": `{"defaults":{"max_active_jobs":2,"max_queued_jobs":2,"max_gpus":{"hyperscaler":2,"neocloud":2,"marketplace":0}},"owners":{"user:1":{"max_active_jobs":1,"reason":"x"}}}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed, want refusal", name)
		}
	}
}

func TestOverrideReplacesOnlyTheFieldsItNames(t *testing.T) {
	c := config(t)
	got := c.For("tenant:7")
	want := Limits{MaxActiveJobs: 8, MaxQueuedJobs: 2, MaxGPUs: map[sku.Tier]int{
		sku.TierHyperscaler: 8, sku.TierNeocloud: 2, sku.TierMarketplace: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tenant:7 = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(c.For("user:7"), c.Defaults) {
		t.Fatal("an owner without an override must get the defaults")
	}
	if c.Defaults.MaxGPUs[sku.TierHyperscaler] != 2 {
		t.Fatal("applying an override mutated the defaults")
	}
}

func TestEvaluateRefusesWholeAndReportsHeadroom(t *testing.T) {
	c := config(t)
	cases := []struct {
		name  string
		owner string
		usage Usage
		sku   string
		want  []Violation
	}{
		{name: "empty account, one GPU", owner: "user:1", sku: "a10g-x1"},
		{name: "second slot", owner: "user:1", sku: "a10g-x1",
			usage: Usage{ActiveJobs: 1, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 1}}},
		// The CoreWeave case: four GPUs asked, two allowed. Refused, not
		// granted as two.
		{name: "job larger than the tier limit", owner: "user:1", sku: "a10g-x4",
			want: []Violation{{Limit: LimitGPUs, Tier: sku.TierHyperscaler, Max: 2, Used: 0, Requested: 4, Headroom: 2}}},
		{name: "jobs full", owner: "user:1", sku: "a100-neo",
			usage: Usage{ActiveJobs: 2, QueuedJobs: 1, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 2}},
			want:  []Violation{{Limit: LimitActiveJobs, Max: 2, Used: 2, Requested: 1, Headroom: 0}}},
		{name: "queue full but jobs free", owner: "tenant:7", sku: "a10g-x1",
			usage: Usage{ActiveJobs: 2, QueuedJobs: 2, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 2}},
			want:  []Violation{{Limit: LimitQueuedJobs, Max: 2, Used: 2, Requested: 1, Headroom: 0}}},
		{name: "tiers are separate", owner: "user:1", sku: "a100-neo",
			usage: Usage{ActiveJobs: 1, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 2}}},
		{name: "marketplace closed by default", owner: "user:1", sku: "rtx-vast",
			want: []Violation{{Limit: LimitGPUs, Tier: sku.TierMarketplace, Max: 0, Used: 0, Requested: 1, Headroom: 0}}},
		{name: "marketplace opened by override", owner: "tenant:7", sku: "rtx-vast"},
		{name: "every violation listed", owner: "user:1", sku: "a10g-x4",
			usage: Usage{ActiveJobs: 2, QueuedJobs: 2, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 2}},
			want: []Violation{
				{Limit: LimitActiveJobs, Max: 2, Used: 2, Requested: 1, Headroom: 0},
				{Limit: LimitQueuedJobs, Max: 2, Used: 2, Requested: 1, Headroom: 0},
				{Limit: LimitGPUs, Tier: sku.TierHyperscaler, Max: 2, Used: 2, Requested: 4, Headroom: 0},
			}},
		{name: "lowered limit never shows negative headroom", owner: "user:1", sku: "a10g-x1",
			usage: Usage{ActiveJobs: 1, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 5}},
			want:  []Violation{{Limit: LimitGPUs, Tier: sku.TierHyperscaler, Max: 2, Used: 5, Requested: 1, Headroom: 0}}},
	}
	for _, tc := range cases {
		if tc.usage.GPUs == nil {
			tc.usage.GPUs = map[sku.Tier]int{}
		}
		err := Evaluate(tc.owner, c.For(tc.owner), tc.usage, item(t, tc.sku))
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: refused: %v", tc.name, err)
			}
			continue
		}
		var ex *ExceededError
		if !errors.As(err, &ex) {
			t.Errorf("%s: err = %v, want *ExceededError", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(ex.Violations, tc.want) {
			t.Errorf("%s: violations = %+v, want %+v", tc.name, ex.Violations, tc.want)
		}
		if !strings.Contains(ex.Error(), "headroom") || !strings.Contains(ex.Error(), tc.sku) {
			t.Errorf("%s: message does not carry the sku and headroom: %s", tc.name, ex.Error())
		}
	}
}

func TestTallyCountsGPUsByTierAcrossActiveJobs(t *testing.T) {
	u, err := Tally(catalogue(t), []store.SKUUsage{
		{SKU: "a10g-x1", Active: 2, Queued: 1},
		{SKU: "a10g-x4", Active: 1},
		{SKU: "a100-neo", Active: 1, Queued: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{ActiveJobs: 4, QueuedJobs: 2, GPUs: map[sku.Tier]int{sku.TierHyperscaler: 6, sku.TierNeocloud: 1}}
	if !reflect.DeepEqual(u, want) {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
}

func TestUnknownActiveSKUFailsClosed(t *testing.T) {
	e := &Enforcer{Config: config(t), Catalogue: catalogue(t), Usage: fixedUsage{{SKU: "deleted-sku", Active: 1}}}
	err := e.Check(context.Background(), "user:1", item(t, "a10g-x1"))
	if !errors.Is(err, ErrUnknownActiveSKU) {
		t.Fatalf("err = %v, want ErrUnknownActiveSKU", err)
	}
}

type fixedUsage []store.SKUUsage

func (f fixedUsage) ActiveUsage(context.Context, string) ([]store.SKUUsage, error) { return f, nil }

func TestAdmitDoesNotHoldWhenOverQuota(t *testing.T) {
	e := &Enforcer{Config: config(t), Catalogue: catalogue(t)}
	var holds int
	hold := func(context.Context) error { holds++; return nil }

	full := []store.SKUUsage{{SKU: "a10g-x1", Active: 2}}
	var ex *ExceededError
	if err := e.Admit("user:1", item(t, "a10g-x1"), hold)(context.Background(), full); !errors.As(err, &ex) {
		t.Fatalf("err = %v, want *ExceededError", err)
	}
	if holds != 0 {
		t.Fatal("hold placed for an over-quota submit")
	}
	if err := e.Admit("user:1", item(t, "a10g-x1"), hold)(context.Background(), nil); err != nil || holds != 1 {
		t.Fatalf("within quota: err=%v holds=%d", err, holds)
	}
	refused := errors.New("402")
	if err := e.Admit("user:1", item(t, "a10g-x1"), func(context.Context) error { return refused })(context.Background(), nil); !errors.Is(err, refused) {
		t.Fatalf("hold refusal must surface unchanged, got %v", err)
	}
}

// End to end against Postgres: the store's admission lock plus this
// package's decision. With one GPU of headroom and many concurrent 1-GPU
// submits, exactly one is admitted and every refusal says headroom 1.
func TestAdmitUnderConcurrencyAgainstPostgres(t *testing.T) {
	url := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	s := openSchema(t, url)
	ctx := context.Background()
	e := &Enforcer{Config: config(t), Catalogue: catalogue(t), Usage: s}
	a10g := item(t, "a10g-x1")

	nj := store.NewJob{Owner: "user:1", SKU: a10g.ID, Spec: []byte(`{"max_runtime_seconds":60}`)}
	if _, _, err := s.CreateAdmitted(ctx, nj, nil, e.Admit("user:1", a10g, nil)); err != nil {
		t.Fatal(err)
	}

	const n = 10
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			_, created, err := s.CreateAdmitted(ctx, nj, nil, e.Admit("user:1", a10g, nil))
			results <- result{created, err}
		}()
	}
	var created int
	for range n {
		r := <-results
		var ex *ExceededError
		switch {
		case r.err == nil && r.created:
			created++
		case errors.As(r.err, &ex):
			if ex.Violations[0].Used != 2 {
				t.Errorf("refusal saw %d in use, want 2 (the winner's job must be visible)", ex.Violations[0].Used)
			}
		default:
			t.Errorf("unexpected: %+v", r)
		}
	}
	if created != 1 {
		t.Fatalf("%d submits admitted into one slot", created)
	}
	if err := e.Check(ctx, "user:1", a10g); err == nil {
		t.Fatal("preflight passes on a full account")
	}
}
