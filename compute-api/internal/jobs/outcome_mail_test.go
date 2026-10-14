package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/mail"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

type sentMail struct {
	kind, to string
	raw      []byte
}

// mailbox is a mailer transport that keeps every message, and refuses while
// refuse is set.
type mailbox struct {
	mu     sync.Mutex
	sent   []sentMail
	refuse bool
}

func (m *mailbox) Deliver(_ context.Context, kind, to string, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuse {
		return errors.New("MessageRejected")
	}
	m.sent = append(m.sent, sentMail{kind, to, raw})
	return nil
}
func (m *mailbox) Check(context.Context) error { return nil }

func (m *mailbox) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

type mailWorld struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	clock *clock
	sky   *skypilottest.Server
	item  sku.SKU
	local *billing.Local
	box   *mailbox
	svc   *jobs.Service
}

const (
	mailAccount = 41
	mailOwner   = "account:41"
)

// newMailWorld is ACCOUNTS_BACKEND=local as main wires it, with a mailer
// whose transport is a mailbox: account 41 with an active owner (ada) and a
// pending one (bob, who has not verified his address and gets nothing).
func newMailWorld(t *testing.T) *mailWorld {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	c := &clock{t: time.Now().Add(time.Minute).Truncate(time.Second)}
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
	sky.SetClock(c.Now)
	t.Cleanup(sky.Close)

	local := billingtest.New(t, st.Pool(), c.Now)
	billingtest.SetBalance(t, st.Pool(), local, mailAccount, 100*sku.QuotaPerUSD)
	t.Cleanup(func() { billingtest.AssertInvariant(t, local) })
	if _, err := st.Pool().Exec(ctx, `INSERT INTO users (account_id, email, password_hash, status, email_verified_at) VALUES
		($1, 'ada@example.com', '$argon2id$test', 'active', now()), ($1, 'bob@example.com', '$argon2id$test', 'pending', NULL)`, mailAccount); err != nil {
		t.Fatal(err)
	}

	box := &mailbox{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &mailer.Mailer{From: mail.Address{Name: "Unify Compute", Address: "no-reply@compute.unifyai.us"},
		AppOrigin: "https://app.compute.unifyai.us", Transport: box, Now: c.Now}
	svc := jobs.New(jobs.Config{
		Store: st, Biller: local, Gate: local, Catalogue: cat, Now: c.Now,
		SkyPilot: &backend.Client{BaseURL: sky.URL, Token: skyToken, UserHash: "unifycmp"},
		Log:      log, Mailer: m, Notifier: notify.New(st.Pool(), log, c.Now),
	})
	return &mailWorld{t: t, ctx: ctx, st: st, clock: c, sky: sky, item: item, local: local, box: box, svc: svc}
}

func (w *mailWorld) submit(maxRuntime int) store.Job {
	w.t.Helper()
	j, _, err := w.svc.Submit(w.ctx, jobs.Submission{
		Owner: mailOwner, UserID: mailAccount, TokenID: 7, SKU: w.item, Candidates: w.item.Providers,
		Spec: backend.Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: maxRuntime},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return j
}

func (w *mailWorld) step() {
	w.svc.Step(w.ctx)
	if err := w.svc.Ledger.Tick(w.ctx); err != nil {
		w.t.Logf("ledger tick: %v", err)
	}
}

func (w *mailWorld) up(id string) {
	w.t.Helper()
	w.sky.Provision(cluster(id))
	w.step()
	w.clock.Advance(time.Minute)
	w.sky.Up(cluster(id), "AWS", "us-east-1", upRes)
	w.step()
	w.sky.SubmitJob(cluster(id))
	start := w.clock.Now().Add(5 * time.Second)
	w.sky.SetJob(cluster(id), "RUNNING", &start, nil)
	w.step()
}

// until steps until the job is terminal and its email, if any, is settled.
func (w *mailWorld) until(id string) store.Job {
	w.t.Helper()
	for range 20 {
		w.clock.Advance(15 * time.Second)
		w.step()
		j, err := w.st.Get(w.ctx, mailOwner, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if j.Status.Terminal() {
			w.svc.WaitMail()
			return j
		}
	}
	w.t.Fatalf("job %s never ended", id)
	return store.Job{}
}

func (w *mailWorld) row(id, message string) (state string, attempts int) {
	w.t.Helper()
	err := w.st.Pool().QueryRow(w.ctx, `SELECT state, attempts FROM notifications WHERE account_id = $1 AND subject_kind = 'job' AND subject_id = $2 AND message = $3`,
		mailAccount, id, message).Scan(&state, &attempts)
	if err != nil {
		w.t.Fatalf("notifications row %s/%s: %v", id, message, err)
	}
	return state, attempts
}

func (w *mailWorld) expectOne(kind string, contains ...string) sentMail {
	w.t.Helper()
	got := w.box.all()
	if len(got) != 1 || got[0].kind != kind || got[0].to != "ada@example.com" {
		w.t.Fatalf("mail %+v, want one %s to ada only", summary(got), kind)
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(string(got[0].raw), "=\r\n", ""), "=3D", "=")
	for _, s := range contains {
		if !strings.Contains(raw, s) {
			w.t.Errorf("%s lacks %q", kind, s)
		}
	}
	return got[0]
}

func summary(ms []sentMail) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.kind+"→"+m.to)
	}
	return out
}

func TestSucceededJobEmailsTheOwnerOnce(t *testing.T) {
	w := newMailWorld(t)
	j := w.submit(600)
	w.up(j.ID)
	w.clock.Advance(10 * time.Minute)
	end := w.clock.Now()
	w.sky.SetJob(cluster(j.ID), "SUCCEEDED", nil, &end)
	if got := w.until(j.ID); got.Status != store.StatusSucceeded {
		t.Fatalf("status %s", got.Status)
	}
	w.expectOne(mailer.KindJobSucceeded, "Unify Compute: job "+j.ID+" finished",
		"https://app.compute.unifyai.us/jobs/"+j.ID, "Status: succeeded", "GPU: a10g-24gb-x1 ", "Cost: USD ")
	if state, n := w.row(j.ID, mailer.KindJobSucceeded); state != "sent" || n != 1 {
		t.Fatalf("row %s/%d", state, n)
	}

	// The settlement loop and the backend keep running after the end.
	// None of it mails again.
	for range 5 {
		w.clock.Advance(5 * time.Minute)
		w.step()
	}
	w.svc.WaitMail()
	if got := w.box.all(); len(got) != 1 {
		t.Fatalf("mailed again: %v", summary(got))
	}
}

func TestJobFailedBeforeUpSaysNothingWasCharged(t *testing.T) {
	w := newMailWorld(t)
	j := w.submit(600)
	w.sky.FailLaunch(cluster(j.ID), "ResourcesUnavailableError", "no capacity", false)
	got := w.until(j.ID)
	if got.Status != store.StatusFailed || got.Error == nil || got.Error.Code != backend.CodeCapacityExhausted {
		t.Fatalf("job %s %+v", got.Status, got.Error)
	}
	w.expectOne(mailer.KindJobFailed, "Unify Compute: job "+j.ID+" failed", "No GPU time was charged.",
		"Reason: capacity_exhausted", "Cost: USD 0.0000", "Status: failed")
}

func TestCustomerCancelIsNotEmailed(t *testing.T) {
	w := newMailWorld(t)
	j := w.submit(600)
	w.up(j.ID)
	if _, err := w.svc.Cancel(w.ctx, mailOwner, j.ID, w.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got := w.until(j.ID); got.Status != store.StatusCancelled {
		t.Fatalf("status %s", got.Status)
	}
	for range 3 {
		w.clock.Advance(time.Minute)
		w.step()
	}
	w.svc.WaitMail()
	if got := w.box.all(); len(got) != 0 {
		t.Fatalf("a customer's own cancel was emailed: %v", summary(got))
	}
}

// The balance runs out under a running job: the extend is refused, the job
// is stopped as balance_exhausted and the owner hears about it once.
func TestBalanceExhaustedJobEmailsTheBillingStop(t *testing.T) {
	w := newMailWorld(t)
	j := w.submit(4 * 3600)
	w.up(j.ID)
	billingtest.SetBalance(t, w.st.Pool(), w.local, mailAccount, 0)
	var got store.Job
	for range 60 {
		w.clock.Advance(5 * time.Minute)
		w.step()
		var err error
		if got, err = w.st.Get(w.ctx, mailOwner, j.ID); err != nil {
			t.Fatal(err)
		}
		if got.Status.Terminal() {
			break
		}
	}
	got = w.until(j.ID)
	if got.Status != store.StatusCancelled || got.Error == nil || got.Error.Code != string(ledger.EndBalanceExhausted) {
		t.Fatalf("job %s %+v", got.Status, got.Error)
	}
	w.expectOne(mailer.KindJobBillingStopped, "Unify Compute: job stopped, balance exhausted", "Reason: balance_exhausted", "Status: cancelled")
}

// SES refusing the message neither fails the transition nor marks it sent:
// the row is failed with its attempt counted, so a later trigger may retry.
func TestRefusedOutcomeEmailLeavesTheJobEndedAndTheRowFailed(t *testing.T) {
	w := newMailWorld(t)
	w.box.refuse = true
	j := w.submit(600)
	w.sky.FailLaunch(cluster(j.ID), "ResourcesUnavailableError", "no capacity", false)
	if got := w.until(j.ID); got.Status != store.StatusFailed {
		t.Fatalf("status %s", got.Status)
	}
	if state, n := w.row(j.ID, mailer.KindJobFailed); state != "failed" || n != 1 {
		t.Fatalf("row %s/%d", state, n)
	}
	if len(w.box.all()) != 0 {
		t.Fatal("a refused message was recorded as delivered")
	}
}
