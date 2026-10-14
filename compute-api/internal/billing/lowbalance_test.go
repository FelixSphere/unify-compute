package billing_test

import (
	"context"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

const lowAccount = 31

type crossings struct {
	mu  sync.Mutex
	got []billing.LowBalanceCrossing
}

func (c *crossings) add(x billing.LowBalanceCrossing) { c.mu.Lock(); defer c.mu.Unlock(); c.got = append(c.got, x) }

func (c *crossings) take() []billing.LowBalanceCrossing {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got = nil
	return out
}

// lowBalanceSubject is a local biller with account lowAccount at USD 20,
// the default threshold of USD 5, and its crossings recorded.
func lowBalanceSubject(t *testing.T) (*pgxpool.Pool, *billing.Local, *crossings) {
	t.Helper()
	db := storetest.New(t).Pool()
	l := billing.New(db, nil)
	c := &crossings{}
	l.OnLowBalance(c.add)
	billingtest.SetBalance(t, db, l, lowAccount, 20*usd)
	t.Cleanup(func() { billingtest.AssertInvariant(t, l) })
	return db, l, c
}

func lowHold(id string, q int64) holdArgs { return holdArgs{id, q} }

type holdArgs struct {
	id string
	q  int64
}

func place(t *testing.T, l *billing.Local, h holdArgs) {
	t.Helper()
	req := holdReq(h.id, h.q)
	req.UserID, req.TokenID, req.ExpiresAt = lowAccount, 0, 0
	if _, err := l.Hold(ctx, req); err != nil {
		t.Fatalf("hold %s: %v", h.id, err)
	}
}

func entryOf(t *testing.T, l *billing.Local, kind, ref string) string {
	t.Helper()
	es, err := l.Entries(ctx, lowAccount)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Kind == kind && e.RefID == ref {
			return e.ID
		}
	}
	t.Fatalf("no %s entry for %s", kind, ref)
	return ""
}

func topup(t *testing.T, db *pgxpool.Pool, l *billing.Local, id string, q int64) {
	t.Helper()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := l.CreditTopupTx(ctx, tx, lowAccount, q, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// The crossing is the hold that takes available from at or above USD 5 to
// below it. Spending further, and a job's hold coming back at its end, do
// not count again; a top-up back above re-arms it, and the next drop is a
// second crossing with its own id.
func TestLowBalanceOneCrossingPerDropAfterATopup(t *testing.T) {
	db, l, c := lowBalanceSubject(t)

	place(t, l, lowHold("hold_a", 15*usd)) // 20 -> 5: at the threshold is not below it
	if got := c.take(); len(got) != 0 {
		t.Fatalf("at the threshold: %+v", got)
	}
	place(t, l, lowHold("hold_b", usd)) // 5 -> 4
	got := c.take()
	if len(got) != 1 {
		t.Fatalf("crossings %+v, want one", got)
	}
	want := billing.LowBalanceCrossing{AccountID: lowAccount, EntryID: entryOf(t, l, "hold", "hold_b"),
		Available: 4 * usd, Held: 16 * usd, Threshold: 5 * usd}
	if got[0] != want {
		t.Fatalf("crossing %+v, want %+v", got[0], want)
	}
	first := got[0].EntryID

	place(t, l, lowHold("hold_c", usd)) // 4 -> 3
	if _, err := l.Release(ctx, "hold_a"); err != nil { // 3 -> 18: a job's end does not re-arm
		t.Fatal(err)
	}
	place(t, l, lowHold("hold_d", 16*usd)) // 18 -> 2
	if got := c.take(); len(got) != 0 {
		t.Fatalf("without a top-up nothing re-arms: %+v", got)
	}

	topup(t, db, l, "tu_low_1", 10*usd) // 2 -> 12
	place(t, l, lowHold("hold_e", 8*usd)) // 12 -> 4
	got = c.take()
	if len(got) != 1 || got[0].EntryID == first || got[0].EntryID != entryOf(t, l, "hold", "hold_e") {
		t.Fatalf("after a top-up: %+v, want one new crossing on hold_e", got)
	}
}

// A top-up that leaves available still below the threshold does not re-arm.
func TestLowBalanceTopupBelowThresholdDoesNotRearm(t *testing.T) {
	db, l, c := lowBalanceSubject(t)
	place(t, l, lowHold("hold_a", 18*usd)) // 20 -> 2: one crossing
	c.take()
	topup(t, db, l, "tu_low_2", 2*usd) // 2 -> 4, still below
	place(t, l, lowHold("hold_b", usd))
	if got := c.take(); len(got) != 0 {
		t.Fatalf("crossings %+v, want none", got)
	}
}

// An extend that crosses counts like a hold.
func TestLowBalanceExtendCrosses(t *testing.T) {
	_, l, c := lowBalanceSubject(t)
	place(t, l, lowHold("hold_a", 10*usd))
	if _, err := l.Extend(ctx, "hold_a", ext("ext_1", 6*usd)); err != nil {
		t.Fatal(err)
	}
	got := c.take()
	if len(got) != 1 || got[0].EntryID != entryOf(t, l, "extend", "hold_a") || got[0].Available != 4*usd {
		t.Fatalf("crossings %+v, want the extend", got)
	}
}

// A threshold of 0 is off.
func TestLowBalanceZeroIsOff(t *testing.T) {
	db, l, c := lowBalanceSubject(t)
	if _, err := db.Exec(ctx, `UPDATE accounts SET low_balance_quota = 0 WHERE id = $1`, lowAccount); err != nil {
		t.Fatal(err)
	}
	place(t, l, lowHold("hold_a", 20*usd))
	if got := c.take(); len(got) != 0 {
		t.Fatalf("crossings %+v, want none", got)
	}
}

// An operator debit runs in the admin CLI's transaction, which the biller
// does not commit, so it reports nothing and leaves the warning armed: the
// next spend reports the crossing.
func TestLowBalanceOperatorDebitLeavesItArmed(t *testing.T) {
	db, l, c := lowBalanceSubject(t)
	billingtest.SetBalance(t, db, l, lowAccount, 3*usd)
	if got := c.take(); len(got) != 0 {
		t.Fatalf("operator debit: %+v", got)
	}
	place(t, l, lowHold("hold_a", usd))
	if got := c.take(); len(got) != 1 {
		t.Fatalf("crossings %+v, want one on the next hold", got)
	}
}

// A refused hold commits nothing of its own and crosses nothing.
func TestLowBalanceRefusedHoldDoesNotCross(t *testing.T) {
	_, l, c := lowBalanceSubject(t)
	req := holdReq("hold_big", 21*usd)
	req.UserID, req.TokenID = lowAccount, 0
	if _, err := l.Hold(ctx, req); err == nil {
		t.Fatal("a hold over the balance was accepted")
	}
	if got := c.take(); len(got) != 0 {
		t.Fatalf("crossings %+v", got)
	}
}

// Holds placed at once from USD 20 to USD 0 cross once between them.
func TestLowBalanceConcurrentHoldsCrossOnce(t *testing.T) {
	_, l, c := lowBalanceSubject(t)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			place(t, l, lowHold("hold_"+string(rune('a'+i)), 2*usd))
		}()
	}
	wg.Wait()
	if got := c.take(); len(got) != 1 || got[0].Available != 4*usd {
		t.Fatalf("crossings %+v, want exactly one, at USD 4", got)
	}
}

type outbox struct {
	mu   sync.Mutex
	sent []string // "kind to"
	raw  [][]byte
}

func (o *outbox) Deliver(_ context.Context, kind, to string, raw []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, kind+" "+to)
	o.raw = append(o.raw, raw)
	return nil
}

func (o *outbox) Check(context.Context) error { return nil }

// Wired as main wires it: each crossing is one email to each active owner,
// and reporting the same crossing again (a retried send) sends nothing more.
func TestLowBalanceMailedOncePerCrossing(t *testing.T) {
	db, l, _ := lowBalanceSubject(t)
	if _, err := db.Exec(ctx, `INSERT INTO users (account_id, email, password_hash, status, email_verified_at) VALUES
		($1, 'ada@example.com', '$argon2id$x', 'active', now()), ($1, 'bob@example.com', '$argon2id$x', 'pending', NULL)`, lowAccount); err != nil {
		t.Fatal(err)
	}
	box := &outbox{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &mailer.Mailer{From: mail.Address{Name: "Unify Compute", Address: "no-reply@compute.unifyai.us"},
		AppOrigin: "https://app.compute.unifyai.us", Transport: box, Now: func() time.Time { return time.Unix(start, 0) }}
	lm := billing.NewLowBalanceMailer(notify.New(db, log, nil), m, log)
	var seen []billing.LowBalanceCrossing
	l.OnLowBalance(func(c billing.LowBalanceCrossing) { seen = append(seen, c); lm.Crossed(c) })

	place(t, l, lowHold("hold_a", 17*usd))
	lm.Wait()
	lm.Crossed(seen[0])
	lm.Wait()
	if len(box.sent) != 1 || box.sent[0] != mailer.KindLowBalance+" ada@example.com" {
		t.Fatalf("sent %v, want one low-balance email to the active owner", box.sent)
	}
	body := strings.ReplaceAll(string(box.raw[0]), "=\r\n", "")
	for _, want := range []string{"USD 3.0000", "USD 17.0000", "USD 5.00", "https://app.compute.unifyai.us/billing"} {
		if !strings.Contains(body, want) {
			t.Errorf("email lacks %q", want)
		}
	}

	topup(t, db, l, "tu_low_3", 10*usd)
	place(t, l, lowHold("hold_b", 9*usd))
	lm.Wait()
	if len(box.sent) != 2 {
		t.Fatalf("sent %v, want a second email for the second crossing", box.sent)
	}
}
