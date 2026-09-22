package skypilot

import (
	"encoding/json"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

func TestSingleCandidateIsFlat(t *testing.T) {
	s := sku.SKU{Accelerator: "A10G", GPUCount: 1, Interruptible: true}
	got, _ := json.Marshal(ResourcesFor(s, []sku.Provider{{Cloud: "aws", InstanceType: "g5.xlarge", Regions: []string{"us-east-1"}}}))
	want := `{"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge","accelerators":"A10G:1","use_spot":true}`
	if string(got) != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestSeveralCandidatesUseAnyOfInOrder(t *testing.T) {
	s := sku.SKU{Accelerator: "L4", GPUCount: 1}
	r := ResourcesFor(s, []sku.Provider{
		{Cloud: "aws", InstanceType: "g6.xlarge", Regions: []string{"us-east-1", "us-west-2"}},
		{Cloud: "kubernetes", Context: "coreweave-ord1"},
	})
	if r.Accelerators != "L4:1" || r.UseSpot || r.Cloud != "" {
		t.Fatalf("shared fields wrong: %+v", r)
	}
	if len(r.AnyOf) != 3 {
		t.Fatalf("want 3 candidates (two regions + one context), got %d", len(r.AnyOf))
	}
	if r.AnyOf[0].Region != "us-east-1" || r.AnyOf[1].Region != "us-west-2" {
		t.Fatalf("region order not preserved: %+v", r.AnyOf)
	}
	if r.AnyOf[2].Cloud != "kubernetes" || r.AnyOf[2].Region != "coreweave-ord1" || r.AnyOf[2].InstanceType != "" {
		t.Fatalf("cluster candidate wrong: %+v", r.AnyOf[2])
	}
}

func TestMultiGPUCount(t *testing.T) {
	s := sku.SKU{Accelerator: "A100-80GB", GPUCount: 8}
	r := ResourcesFor(s, []sku.Provider{{Cloud: "lambda", InstanceType: "gpu_8x_a100_80gb_sxm4"}})
	if r.Accelerators != "A100-80GB:8" || r.Region != "" {
		t.Fatalf("%+v", r)
	}
}
