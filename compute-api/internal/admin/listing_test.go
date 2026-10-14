package admin_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

const listingCatalogue = `{"note":"test","skus":[
  {"id":"a10g-24gb-x1","price_usd_per_hour":"1.2000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":1,
   "providers":[{"cloud":"aws","instance_type":"g5.xlarge","regions":["us-east-1"]}]}]}`

const listingImage = "vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f"

func listingEntry(id string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","title":"Acme","description":"d","image":"` + listingImage + `",
	  "image_source":"https://github.com/example/x","command":["true"],"sku":"a10g-24gb-x1","max_runtime_seconds":600,
	  "inputs":[],"outputs":[],"overridable":[],"available":true}`)
}

type listingEnv struct {
	*env
	store *listings.Store
}

func newListingEnv(t *testing.T, environment string) *listingEnv {
	t.Helper()
	e := newEnv(t)
	cat, err := sku.Parse([]byte(listingCatalogue))
	if err != nil {
		t.Fatal(err)
	}
	set, err := template.Parse([]byte(`{"templates":[]}`), cat, 86400)
	if err != nil {
		t.Fatal(err)
	}
	return &listingEnv{env: e, store: listings.New(e.db, cat, 86400, set, environment, nil)}
}

func (e *listingEnv) run(args ...string) error {
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Mailer: e.mail, Listings: e.store, Actor: "ops-alice", Out: &e.out}
	return cli.Run(ctx, args)
}

func (e *listingEnv) submit(acct int64, id string) listings.Listing {
	e.t.Helper()
	l, err := e.store.Create(ctx, acct, listingEntry(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func TestListingVerbsNeedAMarketplaceAndAReason(t *testing.T) {
	e := newEnv(t)
	if err := e.run("listing", "queue"); err == nil || !strings.Contains(err.Error(), "no account listings") {
		t.Fatalf("%v", err)
	}
	le := newListingEnv(t, "staging")
	acct := le.signup("pat@example.com", true)
	l := le.submit(acct, "acme-embed")
	if err := le.run("listing", "approve", l.ID); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("approved without a reason: %v", err)
	}
	if err := le.run("listing", "show", "acct_1"); err == nil || !strings.Contains(err.Error(), "lst_") {
		t.Fatalf("%v", err)
	}
	if err := le.run("listing", "show", "lst_01ARZ3NDEKTSV4RRFFQ69G5FAV"); err == nil || !strings.Contains(err.Error(), "no such listing") {
		t.Fatalf("%v", err)
	}
}

func TestQueueShowApproveAndTheAuditTrail(t *testing.T) {
	e := newListingEnv(t, "staging")
	acct := e.signup("pat@example.com", true)
	if err := e.run("listing", "queue"); err != nil || !strings.Contains(e.out.String(), "queue is empty") {
		t.Fatalf("%v %q", err, e.out.String())
	}
	l := e.submit(acct, "acme-embed")
	if err := e.run("listing", "queue"); err != nil || !strings.Contains(e.out.String(), "1 waiting for review") ||
		!strings.Contains(e.out.String(), l.ID) || !strings.Contains(e.out.String(), "image source: https://github.com/example/x") {
		t.Fatalf("%v %q", err, e.out.String())
	}
	if err := e.run("listing", "show", l.ID); err != nil || !strings.Contains(e.out.String(), `"id": "acme-embed"`) {
		t.Fatalf("show must print the template as buyers see it: %v %q", err, e.out.String())
	}
	if err := e.run("listing", "approve", l.ID, "--reason", "image is the vLLM release"); err != nil ||
		!strings.Contains(e.out.String(), "approved "+l.ID+" (acme-embed) by ops-alice: image is the vLLM release") {
		t.Fatalf("%v %q", err, e.out.String())
	}
	if err := e.run("listing", "approve", l.ID, "--reason", "again"); err == nil || !strings.Contains(err.Error(), "not waiting for review") {
		t.Fatalf("%v", err)
	}
	pub, err := e.store.Approved(ctx)
	if err != nil || len(pub) != 1 || pub[0].ID != "acme-embed" {
		t.Fatalf("%+v %v", pub, err)
	}
	if err := e.run("listing", "unlist", l.ID, "--reason", "publisher retagged the image"); err != nil || !strings.Contains(e.out.String(), "unlisted "+l.ID) {
		t.Fatalf("%v %q", err, e.out.String())
	}
	if err := e.run("listing", "unlist", l.ID, "--reason", "twice"); err == nil || !strings.Contains(err.Error(), "already unlisted") {
		t.Fatalf("%v", err)
	}
	au := e.audits(acct)
	if len(au) != 2 || au[0].Action != "listing_approved" || au[0].ActorID != "ops-alice" || au[0].Reason != "image is the vLLM release" ||
		au[0].Detail["listing_id"] != l.ID || au[1].Action != "listing_unlisted" || au[1].Reason != "publisher retagged the image" {
		t.Fatalf("audit %+v", au)
	}
}

func TestRejectUnlistsAndProductionRefusesToApprove(t *testing.T) {
	e := newListingEnv(t, "staging")
	acct := e.signup("pat@example.com", true)
	l := e.submit(acct, "acme-embed")
	if err := e.run("listing", "reject", l.ID, "--reason", "image is not from the source named"); err != nil ||
		!strings.Contains(e.out.String(), "rejected "+l.ID) {
		t.Fatalf("%v %q", err, e.out.String())
	}
	got, err := e.store.Get(ctx, acct, l.ID)
	if err != nil || got.ReviewState != listings.Rejected || got.Listed {
		t.Fatalf("%+v %v", got, err)
	}
	if au := e.audits(acct); len(au) != 1 || au[0].Action != "listing_rejected" {
		t.Fatalf("audit %+v", au)
	}

	p := newListingEnv(t, "production")
	pacct := p.signup("prod@example.com", true)
	pl := p.submit(pacct, "acme-prod")
	if err := p.run("listing", "approve", pl.ID, "--reason", "fine"); err == nil || !strings.Contains(err.Error(), "closed in this environment") {
		t.Fatalf("%v", err)
	}
	if err := p.run("listing", "reject", pl.ID, "--reason", "not yet"); err != nil {
		t.Fatalf("rejecting must work everywhere: %v", err)
	}
}
