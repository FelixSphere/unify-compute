package listings_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

var ctx = context.Background()

const catalogue = `{"note":"test","skus":[
  {"id":"a10g-24gb-x1","price_usd_per_hour":"1.2000","available":true,"tier":"hyperscaler","accelerator":"A10G","gpu_count":1,
   "providers":[{"cloud":"aws","instance_type":"g5.xlarge","regions":["us-east-1"]}]},
  {"id":"l4-24gb-x1","price_usd_per_hour":"0.9000","available":false,"tier":"hyperscaler","accelerator":"L4",
   "providers":[{"cloud":"gcp","instance_type":"g2-standard-4"}]}
]}`

const image = "vllm/vllm-openai:v0.30.0-cu129@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f"

const platform = `{"templates":[{"id":"embed","title":"Embed","description":"d","image":"` + image + `",
  "command":["true"],"sku":"a10g-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":[],"available":true}]}`

// entry is one templates.json entry an account might list.
func entry(id, title, skuID string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","title":"` + title + `","description":"d","image":"` + image + `",
	  "image_licence":"Apache-2.0","image_source":"https://github.com/example/x","command":["true"],"env":{"MODEL":"m"},
	  "sku":"` + skuID + `","max_runtime_seconds":600,"inputs":[{"mount":"/mnt/in","description":"in"}],
	  "outputs":[{"mount":"/mnt/out","description":"out"}],"overridable":["sku","spec.env"],"available":true}`)
}

type env struct {
	t     *testing.T
	st    *store.Store
	open  *listings.Store
	prod  *listings.Store
	acct  int64
	other int64
	audit []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	cat, err := sku.Parse([]byte(catalogue))
	if err != nil {
		t.Fatal(err)
	}
	set, err := template.Parse([]byte(platform), cat, 86400)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st}
	// Each call is a second later, so "oldest first" is observable.
	tick := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { tick = tick.Add(time.Second); return tick }
	e.open = listings.New(st.Pool(), cat, 86400, set, "staging", now)
	e.prod = listings.New(st.Pool(), cat, 86400, set, "production", now)
	e.acct, e.other = e.account(), e.account()
	return e
}

func (e *env) account() int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Pool().QueryRow(ctx, `INSERT INTO accounts (compute_enabled, created_at) VALUES (true, now()) RETURNING id`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) recordAudit(_ context.Context, _ pgx.Tx, accountID int64, action, reason string, detail map[string]any) error {
	e.audit = append(e.audit, action+":"+reason+":"+detail["template_id"].(string))
	return nil
}

func (e *env) create(id string) listings.Listing {
	e.t.Helper()
	l, err := e.open.Create(ctx, e.acct, entry(id, "T", "a10g-24gb-x1"))
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func TestValidateAppliesThePlatformRulesAndRefusesPlatformIDs(t *testing.T) {
	e := newEnv(t)
	t.Run("valid", func(t *testing.T) {
		tpl, err := e.open.Validate(entry("acme-embed", "Acme", "a10g-24gb-x1"))
		if err != nil || tpl.ID != "acme-embed" || tpl.Publisher != template.PublisherOfRecord || !tpl.Available {
			t.Fatalf("%+v %v", tpl, err)
		}
	})
	cases := []struct {
		name  string
		raw   json.RawMessage
		code  string
		field string
	}{
		{"platform id", entry("embed", "T", "a10g-24gb-x1"), "listing_id_taken", "template.id"},
		{"not an object", json.RawMessage(`"embed"`), "invalid_listing", "template"},
		{"unknown sku", entry("x", "T", "h100-80gb-x8"), "invalid_listing", "template"},
		{"bad id", entry("Not A Slug", "T", "a10g-24gb-x1"), "invalid_listing", "template"},
		{"unpinned image", json.RawMessage(`{"id":"x","title":"T","description":"d","image":"vllm/vllm-openai:latest","command":["true"],
		  "sku":"a10g-24gb-x1","max_runtime_seconds":600,"inputs":[],"outputs":[],"overridable":[],"available":true}`), "invalid_listing", "template"},
	}
	for _, tc := range cases {
		_, err := e.open.Validate(tc.raw)
		var inv *listings.Invalid
		if !errors.As(err, &inv) || inv.Code != tc.code || inv.Field != tc.field {
			t.Errorf("%s: got %v, want %s on %s", tc.name, err, tc.code, tc.field)
		}
	}
}

func TestCreateStartsPendingAndListedAndTheIDIsUnique(t *testing.T) {
	e := newEnv(t)
	l := e.create("acme-embed")
	if !strings.HasPrefix(l.ID, "lst_") || l.ReviewState != listings.Pending || !l.Listed || l.Public() || l.Runs != 0 ||
		l.AccountID != e.acct || l.Template.Publisher != template.PublisherOfRecord || l.Template.Object != "compute.template" {
		t.Fatalf("%+v", l)
	}
	if _, err := e.open.Create(ctx, e.other, entry("acme-embed", "Other", "a10g-24gb-x1")); !errors.Is(err, listings.ErrExists) {
		t.Fatalf("second account took the same id: %v", err)
	}
	ls, err := e.open.ByAccount(ctx, e.acct)
	if err != nil || len(ls) != 1 || ls[0].ID != l.ID {
		t.Fatalf("%+v %v", ls, err)
	}
	if ls, err := e.open.ByAccount(ctx, e.other); err != nil || len(ls) != 0 {
		t.Fatalf("the other account sees it: %+v %v", ls, err)
	}
	if _, err := e.open.Get(ctx, e.other, l.ID); !errors.Is(err, listings.ErrNotFound) {
		t.Fatalf("the other account can read it: %v", err)
	}
}

func TestOnlyApprovedAndListedReachBuyersAndOnlyWhereOpen(t *testing.T) {
	e := newEnv(t)
	l := e.create("acme-embed")
	if got, _ := e.open.Approved(ctx); len(got) != 0 {
		t.Fatalf("pending served: %+v", got)
	}
	if _, err := e.open.Template(ctx, "acme-embed"); !errors.Is(err, template.ErrUnknown) {
		t.Fatalf("pending resolvable: %v", err)
	}
	if _, err := e.prod.Review(ctx, l.ID, true, "ops", "looks fine", e.recordAudit); !errors.Is(err, listings.ErrClosed) {
		t.Fatalf("production approved: %v", err)
	}
	if _, err := e.open.Review(ctx, l.ID, true, "ops", " ", e.recordAudit); err == nil {
		t.Fatal("approved without a reason")
	}
	got, err := e.open.Review(ctx, l.ID, true, "ops", "image is the vLLM release", e.recordAudit)
	if err != nil || got.ReviewState != listings.Approved || !got.Public() || got.ReviewedBy != "ops" || got.ReviewedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	if len(e.audit) != 1 || e.audit[0] != "listing_approved:image is the vLLM release:acme-embed" {
		t.Fatalf("audit %v", e.audit)
	}
	pub, err := e.open.Approved(ctx)
	if err != nil || len(pub) != 1 || pub[0].ID != "acme-embed" || pub[0].Publisher != template.PublisherOfRecord {
		t.Fatalf("%+v %v", pub, err)
	}
	if tpl, err := e.open.Template(ctx, "acme-embed"); err != nil || tpl.ID != "acme-embed" || !tpl.Available {
		t.Fatalf("%+v %v", tpl, err)
	}
	// The same rows, read where account listings are closed: nothing.
	if pub, _ := e.prod.Approved(ctx); len(pub) != 0 {
		t.Fatalf("production serves listings: %+v", pub)
	}
	if _, err := e.prod.Template(ctx, "acme-embed"); !errors.Is(err, template.ErrUnknown) {
		t.Fatalf("production resolves listings: %v", err)
	}
	if _, err := e.open.Review(ctx, l.ID, true, "ops", "again", e.recordAudit); !errors.Is(err, listings.ErrNotPending) {
		t.Fatalf("approved twice: %v", err)
	}
}

func TestAnyEditReturnsAnApprovedListingToPending(t *testing.T) {
	e := newEnv(t)
	l := e.create("acme-embed")
	if _, err := e.open.Review(ctx, l.ID, true, "ops", "ok", nil); err != nil {
		t.Fatal(err)
	}
	got, err := e.open.Update(ctx, e.acct, l.ID, entry("acme-embed", "Acme v2", "a10g-24gb-x1"))
	if err != nil || got.ReviewState != listings.Pending || got.ReviewReason != "" || got.ReviewedAt != nil || !got.Listed || got.Public() ||
		got.Template.Title != "Acme v2" {
		t.Fatalf("%+v %v", got, err)
	}
	if pub, _ := e.open.Approved(ctx); len(pub) != 0 {
		t.Fatalf("still served after the edit: %+v", pub)
	}
	_, err = e.open.Update(ctx, e.acct, l.ID, entry("acme-embed-2", "T", "a10g-24gb-x1"))
	var inv *listings.Invalid
	if !errors.As(err, &inv) || inv.Code != "listing_id_fixed" {
		t.Fatalf("id change: %v", err)
	}
	if _, err := e.open.Update(ctx, e.other, l.ID, entry("acme-embed", "T", "a10g-24gb-x1")); !errors.Is(err, listings.ErrNotFound) {
		t.Fatalf("the other account edited it: %v", err)
	}
}

func TestRejectionUnlistsAndCarriesTheReason(t *testing.T) {
	e := newEnv(t)
	l := e.create("acme-embed")
	got, err := e.open.Review(ctx, l.ID, false, "ops", "image is not from the source you named", e.recordAudit)
	if err != nil || got.ReviewState != listings.Rejected || got.Listed || got.ReviewReason != "image is not from the source you named" {
		t.Fatalf("%+v %v", got, err)
	}
	if q, _ := e.open.Queue(ctx); len(q) != 0 {
		t.Fatalf("rejected still queued: %+v", q)
	}
	// The publisher changes it and asks again: a fresh request.
	again, err := e.open.Update(ctx, e.acct, l.ID, entry("acme-embed", "Fixed", "a10g-24gb-x1"))
	if err != nil || again.ReviewState != listings.Pending || !again.Listed || again.ReviewReason != "" {
		t.Fatalf("%+v %v", again, err)
	}
	if q, _ := e.open.Queue(ctx); len(q) != 1 || q[0].ID != l.ID {
		t.Fatalf("queue %+v", q)
	}
}

func TestUnlistingIsNeverRefusedAndTheAdminUnlistIsAudited(t *testing.T) {
	e := newEnv(t)
	l := e.create("acme-embed")
	if _, err := e.open.Review(ctx, l.ID, true, "ops", "ok", nil); err != nil {
		t.Fatal(err)
	}
	got, err := e.open.Unlist(ctx, e.acct, l.ID)
	if err != nil || got.Listed || got.ReviewState != listings.Approved || got.Public() {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := e.open.Unlist(ctx, e.acct, l.ID); err != nil || got.Listed {
		t.Fatalf("second unlist: %+v %v", got, err)
	}
	if _, err := e.open.AdminUnlist(ctx, l.ID, "ops", "already off", e.recordAudit); !errors.Is(err, listings.ErrNotListed) {
		t.Fatalf("%v", err)
	}
	// Relist (pending again), approve, then the super admin pulls it.
	if _, err := e.open.Update(ctx, e.acct, l.ID, entry("acme-embed", "T", "a10g-24gb-x1")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.open.Review(ctx, l.ID, true, "ops", "ok", nil); err != nil {
		t.Fatal(err)
	}
	got, err = e.open.AdminUnlist(ctx, l.ID, "ops", "publisher's image was retagged", e.recordAudit)
	if err != nil || got.Listed {
		t.Fatalf("%+v %v", got, err)
	}
	if len(e.audit) != 1 || e.audit[0] != "listing_unlisted:publisher's image was retagged:acme-embed" {
		t.Fatalf("audit %v", e.audit)
	}
	if pub, _ := e.open.Approved(ctx); len(pub) != 0 {
		t.Fatalf("unlisted still served: %+v", pub)
	}
}

func TestQueueIsOldestFirstAndRunsCountJobsByTemplateID(t *testing.T) {
	e := newEnv(t)
	first := e.create("acme-a")
	second := e.create("acme-b")
	q, err := e.open.Queue(ctx)
	if err != nil || len(q) != 2 || q[0].ID != first.ID || q[1].ID != second.ID {
		t.Fatalf("%+v %v", q, err)
	}
	tid := "acme-a"
	for i := 0; i < 2; i++ {
		if _, _, err := e.st.Create(ctx, store.NewJob{Owner: "user:9", SKU: "a10g-24gb-x1", Spec: json.RawMessage(`{}`), TemplateID: &tid}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.st.Create(ctx, store.NewJob{Owner: "user:9", SKU: "a10g-24gb-x1", Spec: json.RawMessage(`{}`)}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := e.open.Get(ctx, e.acct, first.ID)
	if err != nil || got.Runs != 2 {
		t.Fatalf("runs %+v %v", got, err)
	}
}

func TestAvailabilityFollowsTheCatalogueAtReadTime(t *testing.T) {
	e := newEnv(t)
	l, err := e.open.Create(ctx, e.acct, entry("acme-l4", "T", "l4-24gb-x1"))
	if err != nil || l.Template.Available || l.Template.UnavailableReason == "" {
		t.Fatalf("a listing on an unsold sku must read as unavailable: %+v %v", l, err)
	}
}
