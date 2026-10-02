package upstream

import (
	"errors"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

var l4 = sku.SKU{ID: "l4", Accelerator: "L4", GPUCount: 1, Providers: []sku.Provider{
	{Cloud: "aws", InstanceType: "g6.xlarge", Regions: []string{"us-east-1"}},
	{Cloud: "gcp", InstanceType: "g2-standard-4", Regions: []string{"us-central1"}},
}}

func TestParseRejectsUnknownAndAcceptsEmpty(t *testing.T) {
	if _, err := Parse("aws, openai"); err == nil {
		t.Fatal("unknown cloud accepted")
	}
	r, err := Parse("")
	if err != nil || len(r.Clouds()) != 0 {
		t.Fatalf("empty registry: %v %v", r.Clouds(), err)
	}
	r, _ = Parse(" GCP,aws ")
	if got := r.Clouds(); len(got) != 2 || got[0] != "aws" || got[1] != "gcp" {
		t.Fatalf("clouds %v", got)
	}
}

func TestPlaceNarrowsToEnabled(t *testing.T) {
	r, _ := Parse("aws")
	res, live, err := r.Place(l4, nil)
	if err != nil || len(live) != 1 || live[0].Cloud != "aws" || res.Cloud != "aws" {
		t.Fatalf("%+v %v %v", res, live, err)
	}
}

func TestPlaceDistinguishesCustomerAndDeploymentErrors(t *testing.T) {
	r, _ := Parse("aws")
	if _, _, err := r.Place(l4, []string{"azure"}); !errors.Is(err, sku.ErrProviderNotOffered) {
		t.Fatalf("want provider-not-offered, got %v", err)
	}
	if _, _, err := r.Place(l4, []string{"gcp"}); !errors.Is(err, ErrNoEnabledProvider) {
		t.Fatalf("want no-enabled-provider, got %v", err)
	}
	empty, _ := Parse("")
	if _, _, err := empty.Place(l4, nil); !errors.Is(err, ErrNoEnabledProvider) {
		t.Fatalf("empty registry must refuse: %v", err)
	}
}
