// Package billingtest gives tests a local biller on a migrated database, and
// a consoletest.Book backed by it, so tests written against the console fake
// run unchanged against the local biller.
package billingtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// LocalBillerPrefix marks the parity run: a test whose name starts with it
// builds its console fake on the local biller (Parity). Test helpers check it
// so the same test bodies run against both.
const LocalBillerPrefix = "TestLocalBiller/"

// Parity reports whether t is part of a local-biller parity run.
func Parity(t *testing.T) bool { return strings.HasPrefix(t.Name(), LocalBillerPrefix) }

// New returns a local biller on db, a store migrated through 0006 (the
// accounts of 0005 and the ledger of 0006).
func New(t testing.TB, db *pgxpool.Pool, now func() time.Time) *billing.Local {
	t.Helper()
	return billing.New(db, now)
}

// Account creates account id (active, compute enabled) if it does not exist.
// The id is forced so the console fake's user ids can name accounts; real
// accounts get theirs from the identity (internal/accounts).
func Account(t testing.TB, db *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO accounts (id, status, compute_enabled) OVERRIDING SYSTEM VALUE VALUES ($1, 'active', true)
		ON CONFLICT (id) DO NOTHING`, id)
	if err != nil {
		t.Fatalf("billingtest: create account %d: %v", id, err)
	}
}

// SetBalance moves the account's available balance to quota with an
// admin_credit or admin_debit entry, so the balance invariant holds through
// a test that reseeds a balance mid-run.
func SetBalance(t testing.TB, db *pgxpool.Pool, l *billing.Local, id int64, quota int64) {
	t.Helper()
	ctx := context.Background()
	Account(t, db, id)
	b, err := l.Balance(ctx, id)
	if err != nil {
		t.Fatalf("billingtest: balance: %v", err)
	}
	if b.Available == quota {
		return
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	ref := store.NewID("test_", time.Now())
	if _, err := l.AdjustTx(ctx, tx, billing.Adjustment{AccountID: id, Quota: quota - b.Available, RefType: "test", RefID: ref}); err != nil {
		t.Fatalf("billingtest: set balance: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// AssertInvariant fails t when any account's balance differs from the sum
// of its entries.
func AssertInvariant(t testing.TB, l *billing.Local) {
	t.Helper()
	bad, err := l.Audit(context.Background(), nil)
	if err != nil {
		t.Fatalf("billingtest: audit: %v", err)
	}
	for _, m := range bad {
		t.Errorf("account %d: available %d held %d, entries sum to %d / %d", m.AccountID, m.Available, m.Held, m.SumAvailable, m.SumHeld)
	}
}

// Book adapts a local biller to consoletest.Book. Console user ids are used
// as account ids, which is what holds.user_id becomes after the cutover.
func Book(t testing.TB, db *pgxpool.Pool, now func() time.Time) (consoletest.Book, *billing.Local) {
	t.Helper()
	l := New(t, db, now)
	return &book{Local: l, t: t, db: db}, l
}

type book struct {
	*billing.Local
	t  testing.TB
	db *pgxpool.Pool
}

func (b *book) Sweep(ctx context.Context) error {
	_, err := b.Local.Sweep(ctx)
	return err
}

func (b *book) SetBalance(userID int, quota int64) {
	SetBalance(b.t, b.db, b.Local, int64(userID), quota)
}

func (b *book) Balance(userID int) int64 {
	bal, err := b.Local.Balance(context.Background(), int64(userID))
	if err != nil {
		return 0
	}
	return bal.Available
}

var statuses = map[string]consoletest.HoldStatus{
	billing.HoldOpen:     consoletest.HoldActive,
	billing.HoldSettled:  consoletest.HoldSettled,
	billing.HoldReleased: consoletest.HoldReleased,
	billing.HoldExpired:  consoletest.HoldExpired,
}

func (b *book) Inspect(holdID string) (consoletest.Hold, bool) {
	h, ok, err := b.HoldRecord(context.Background(), holdID)
	if err != nil {
		b.t.Errorf("billingtest: hold %s: %v", holdID, err)
	}
	if !ok {
		return consoletest.Hold{}, false
	}
	return consoletest.Hold{HoldID: h.HoldID, UserID: int(h.AccountID), TokenID: int(h.KeyID), JobID: h.JobID, SKU: h.SKU,
		Quota: h.Quota, Settled: h.Settled, Released: h.Released, Status: statuses[h.Status], ExpiresAt: h.ExpiresAt}, true
}

func (b *book) ConsumeLogs() []consoletest.ConsumeLog {
	ss, err := b.Settlements(context.Background())
	if err != nil {
		b.t.Errorf("billingtest: settlements: %v", err)
	}
	out := make([]consoletest.ConsumeLog, len(ss))
	for i, s := range ss {
		out[i] = consoletest.ConsumeLog{UserID: int(s.AccountID), TokenID: int(s.KeyID), ModelName: "compute/" + s.SKU,
			Quota: s.Quota, GPUSeconds: s.GPUSeconds, JobID: s.JobID, HoldID: s.HoldID, EventID: s.EventID}
	}
	return out
}
