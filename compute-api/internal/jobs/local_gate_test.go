package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

// ACCOUNTS_BACKEND=local as main wires it: the local biller is both the
// ledger's biller and the submit gate, and no console exists at all. A
// submit from an account without compute enabled (ADR-0011 D3) or a
// suspended one is refused before quota and the hold, and persists nothing;
// an enabled one is held against the account's own balance.
func TestSubmitWithTheLocalBillerGatesAndHoldsLocally(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := func() time.Time { return time.Now().Add(time.Minute) }
	cat, err := sku.Load(filepath.Join("..", "..", "config", "skus.json"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := cat.Get(skuID)
	if err != nil {
		t.Fatal(err)
	}
	sky := skypilottest.New()
	sky.Token = skyToken
	t.Cleanup(sky.Close)

	const account = 41
	local := billingtest.New(t, st.Pool(), now)
	billingtest.SetBalance(t, st.Pool(), local, account, 100*sku.QuotaPerUSD)
	t.Cleanup(func() { billingtest.AssertInvariant(t, local) })
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET compute_enabled = false WHERE id = $1`, account); err != nil {
		t.Fatal(err)
	}

	svc := jobs.New(jobs.Config{
		Store: st, Biller: local, Gate: local, Catalogue: cat, Now: now,
		SkyPilot: &backend.Client{BaseURL: sky.URL, Token: skyToken, UserHash: "unifycmp"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	sub := jobs.Submission{
		Owner: "account:41", UserID: account, TokenID: 7, SKU: item, Candidates: item.Providers,
		Spec: backend.Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: 600},
	}
	countJobs := func() int {
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if _, _, err := svc.Submit(ctx, sub); !errors.Is(err, billing.ErrComputeNotEnabled) {
		t.Fatalf("compute off: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET compute_enabled = true, status = 'suspended', suspended_reason = 'operator' WHERE id = $1`, account); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Submit(ctx, sub); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("suspended: %v", err)
	}
	if n := countJobs(); n != 0 {
		t.Fatalf("a refused submit persisted %d jobs", n)
	}
	if b, _ := local.Balance(ctx, account); b.Held != 0 {
		t.Fatalf("a refused submit held %d", b.Held)
	}

	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET status = 'active', suspended_reason = NULL WHERE id = $1`, account); err != nil {
		t.Fatal(err)
	}
	j, created, err := svc.Submit(ctx, sub)
	if err != nil || !created || j.Status != store.StatusQueued && j.Status != store.StatusProvisioning {
		t.Fatalf("submit: %+v %v %v", j, created, err)
	}
	hd, err := svc.Ledger.Hold(ctx, j.ID)
	if err != nil || hd.State != "open" {
		t.Fatalf("ledger hold %+v %v", hd, err)
	}
	rec, ok, err := local.HoldRecord(ctx, hd.HoldID)
	want, _ := item.EstimateQuota(600)
	if err != nil || !ok || rec.Status != billing.HoldOpen || rec.AccountID != account || rec.JobID != j.ID || rec.Quota != want {
		t.Fatalf("local hold %+v %v %v, want %d open for %s", rec, ok, err, want, j.ID)
	}
	if b, _ := local.Balance(ctx, account); b.Held != want || b.Available != 100*sku.QuotaPerUSD-want {
		t.Fatalf("balance %+v", b)
	}
}
