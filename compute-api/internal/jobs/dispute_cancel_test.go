package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments/stripetest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

// A dispute on a top-up cancels the account's live jobs through the normal
// cancel path (task B2): the job ends cancelled, its hold closes, and the
// account cannot submit again while suspended.
func TestDisputeCancelsLiveJobsThroughTheCancelPath(t *testing.T) {
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

	const account = 52
	local := billingtest.New(t, st.Pool(), now)
	billingtest.SetBalance(t, st.Pool(), local, account, 100*sku.QuotaPerUSD)
	t.Cleanup(func() { billingtest.AssertInvariant(t, local) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := jobs.New(jobs.Config{
		Store: st, Biller: local, Gate: local, Catalogue: cat, Now: now,
		SkyPilot: &backend.Client{BaseURL: sky.URL, Token: skyToken, UserHash: "unifycmp"}, Log: log,
	})
	sub := jobs.Submission{
		Owner: accounts.OwnerFor(account), UserID: account, TokenID: 7, SKU: item, Candidates: item.Providers,
		Spec: backend.Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: 600},
	}
	j, _, err := svc.Submit(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	svc.Step(ctx)
	if live, _ := st.Get(ctx, sub.Owner, j.ID); live.Status.Terminal() {
		t.Fatalf("job ended before the dispute: %s", live.Status)
	}

	// A paid top-up of USD 10 for the account, as the webhook leaves one.
	tu := store.NewID("tu_", now())
	if _, err := st.Pool().Exec(ctx, `INSERT INTO topups (id, account_id, user_id, amount_cents, status, stripe_checkout_session_id,
		stripe_payment_intent_id, created_at, paid_at) VALUES ($1, $2, 1, 1000, 'credited', 'cs_x', 'pi_dispute', $3, $3)`, tu, account, now()); err != nil {
		t.Fatal(err)
	}
	pay := payments.New(payments.Config{DB: st.Pool(), Accounts: accounts.New(st.Pool(), accounts.Config{}), Billing: local,
		Stripe: &payments.Stripe{Key: stripetest.Key}, Jobs: svc, Log: log, Now: time.Now,
		Settings: payments.Settings{SecretKey: stripetest.Key, WebhookSecret: stripetest.Secret, Environment: "staging"}})
	body := stripetest.Event("evt_dispute_1", "charge.dispute.created", false, stripetest.DisputeObject("dp_job", "pi_dispute", 1000, "needs_response"))
	out, err := pay.HandleWebhook(ctx, payments.Sign(body, stripetest.Secret, time.Now()), body)
	pay.Wait()
	if err != nil || out != payments.OutcomeDisputed {
		t.Fatalf("dispute: %s %v", out, err)
	}
	for range 3 {
		svc.Step(ctx)
	}
	got, err := st.Get(ctx, sub.Owner, j.ID)
	if err != nil || got.Status != store.StatusCancelled {
		t.Fatalf("job after dispute: %s %v", got.Status, err)
	}
	h, err := svc.Ledger.Hold(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, err := local.HoldRecord(ctx, h.HoldID)
	if err != nil || !ok || rec.Status == billing.HoldOpen {
		t.Fatalf("hold after cancel %+v %v %v", rec, ok, err)
	}
	if b, _ := local.Balance(ctx, account); b.Held != 0 {
		t.Fatalf("still held %d", b.Held)
	}
	if _, _, err := svc.Submit(ctx, sub); err == nil {
		t.Fatal("a suspended account submitted a job")
	}
}
