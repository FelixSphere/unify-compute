package billing_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func outcome(v any, err error) string {
	var ce *console.Error
	switch {
	case err == nil:
		return fmt.Sprintf("ok %+v", v)
	case errors.As(err, &ce):
		return fmt.Sprintf("%d %s", ce.Status, ce.Message)
	}
	return "error " + err.Error()
}

// The same random sequence of holds, settles, extends, releases, replays and
// clock jumps, sent to the console fake and to the local biller, gets the
// same answer at every step and leaves the same balances and holds; and the
// local balance is always the sum of its entries.
//
// One timing difference is allowed: the console sweeps every expired hold
// before each request, including one naming a hold that does not exist,
// while the local biller expires the holds of the account a call touches
// (and all of them hourly). A hold_not_found call names no account, so
// after one the two may differ until the next call that touches the account
// or a sweep, and the state is compared again from there.
func TestRandomSequenceMatchesTheConsoleFake(t *testing.T) {
	for seed := int64(1); seed <= 10; seed++ {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			t.Parallel()
			fc := &clock{}
			fc.Set(start)
			fake := fakeSubject(t, fc, nil)
			local, l := localSubject(t)
			rng := rand.New(rand.NewSource(seed))

			holds := []string{"hold_a", "hold_b", "hold_c", "hold_d"}
			var events, extends []string
			now := start
			synced := true
			pick := func(xs []string, fresh string) string {
				if len(xs) == 0 || rng.Intn(3) == 0 {
					return fresh
				}
				return xs[rng.Intn(len(xs))]
			}
			for step := 0; step < 300; step++ {
				var op string
				var a, b string
				switch r := rng.Intn(10); {
				case r < 2:
					id := holds[rng.Intn(len(holds))]
					req := holdReq(id, int64(rng.Intn(8)+1)*usd/2)
					req.ExpiresAt = start + int64(rng.Intn(4)+1)*1800
					if rng.Intn(5) == 0 {
						req.ExpiresAt = 0
					}
					op = fmt.Sprintf("hold %+v", req)
					a, b = outcome(fake.Hold(ctx, req)), outcome(local.Hold(ctx, req))
				case r < 5:
					id := holds[rng.Intn(len(holds))]
					ev := pick(events, fmt.Sprintf("ue_%d", step))
					events = append(events, ev)
					req := settle(ev, int64(rng.Intn(10))*usd/4)
					req.GPUSeconds = int64(rng.Intn(5000))
					req.Final = rng.Intn(6) == 0
					if rng.Intn(15) == 0 {
						req.SKU = otherSKU
					}
					op = fmt.Sprintf("settle %s %+v", id, req)
					a, b = outcome(fake.Settle(ctx, id, req)), outcome(local.Settle(ctx, id, req))
				case r < 7:
					id := holds[rng.Intn(len(holds))]
					x := pick(extends, fmt.Sprintf("ext_%d", step))
					extends = append(extends, x)
					req := ext(x, int64(rng.Intn(4)+1)*usd/2)
					op = fmt.Sprintf("extend %s %+v", id, req)
					a, b = outcome(fake.Extend(ctx, id, req)), outcome(local.Extend(ctx, id, req))
				case r < 9:
					id := holds[rng.Intn(len(holds))]
					op = "release " + id
					a, b = outcome(fake.Release(ctx, id)), outcome(local.Release(ctx, id))
				default:
					now += int64(rng.Intn(1200))
					fake.clock.Set(now)
					local.clock.Set(now)
					op = fmt.Sprint("clock ", now)
					if rng.Intn(2) == 0 {
						fake.sweep()
						local.sweep()
						op += " + sweep"
					}
				}
				switch {
				case a == "404 hold_not_found":
					synced = false
				case a != "":
					synced = true
				case strings.HasSuffix(op, "sweep"):
					synced = true
				}
				t.Logf("step %d %s -> %s | %s (fake %d local %d)", step, op, a, b, fake.balance(), local.balance())
				if a != b {
					t.Fatalf("step %d %s:\n  console fake: %s\n  local:        %s", step, op, a, b)
				}
				if !synced {
					continue
				}
				if fb, lb := fake.balance(), local.balance(); fb != lb {
					t.Fatalf("step %d %s: balance %d on the fake, %d locally", step, op, fb, lb)
				}
				for _, id := range holds {
					if fh, lh := fake.hold(id), local.hold(id); fh != lh {
						t.Fatalf("step %d %s: hold %s\n  console fake: %+v\n  local:        %+v", step, op, id, fh, lh)
					}
				}
			}
			bad, err := l.Audit(ctx, nil)
			if err != nil || len(bad) > 0 {
				t.Fatalf("invariant: %+v %v", bad, err)
			}
		})
	}
}

// Concurrent retries of one request move the balance once, and concurrent
// holds on one account never spend more than it has.
func TestConcurrentRequestsMoveTheBalanceOnce(t *testing.T) {
	s, _ := localSubject(t)
	run := func(n int, f func(i int) error) []error {
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = f(i)
			}()
		}
		wg.Wait()
		return errs
	}
	for _, err := range run(16, func(int) error { _, err := s.Hold(ctx, holdReq("hold_1", 2*usd)); return err }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.balance() != 23*usd {
		t.Fatalf("retried hold reserved more than once: %d", s.balance())
	}
	for _, err := range run(16, func(int) error { _, err := s.Extend(ctx, "hold_1", ext("ext_1", usd)); return err }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range run(16, func(int) error { _, err := s.Settle(ctx, "hold_1", settle("ue_1", usd)); return err }) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if h := s.hold("hold_1"); h.Quota != 3*usd || h.Settled != usd || len(s.logs()) != 1 {
		t.Fatalf("hold %+v logs %d", h, len(s.logs()))
	}
	ok := 0
	for _, err := range run(20, func(i int) error { _, err := s.Hold(ctx, holdReq(fmt.Sprint("hold_n", i), 2*usd)); return err }) {
		switch {
		case err == nil:
			ok++
		default:
			refusal(t, err, 402, billing.CodeInsufficientBalance)
		}
	}
	if ok != 11 || s.balance() != 0 {
		t.Fatalf("%d of 20 holds of USD 2 placed on USD 22, balance %d", ok, s.balance())
	}
}

func TestBalanceEntriesAreAppendOnly(t *testing.T) {
	st := storetest.New(t)
	l := billingtest.New(t, st.Pool(), nil)
	billingtest.SetBalance(t, st.Pool(), l, 7, 5*usd)
	for _, q := range []string{
		`UPDATE balance_entries SET available_delta = 0`,
		`DELETE FROM balance_entries`,
		`TRUNCATE balance_entries`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: want the append-only refusal, got %v", q, err)
		}
	}
}

func TestEntriesRecordEachMovementWithSnapshots(t *testing.T) {
	s, l := localSubject(t)
	if _, err := s.Hold(ctx, holdReq("hold_1", 2*usd)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Extend(ctx, "hold_1", ext("ext_1", usd)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(ctx, "hold_1", settle("ue_1", usd)); err != nil {
		t.Fatal(err)
	}
	final := settle("ue_2", usd)
	final.Final = true
	if _, err := s.Settle(ctx, "hold_1", final); err != nil {
		t.Fatal(err)
	}
	es, err := l.Entries(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		kind               string
		da, dh, after, hel int64
		key                string
	}
	want := []row{
		{"admin_credit", 25 * usd, 0, 25 * usd, 0, ""},
		{"hold", -2 * usd, 2 * usd, 23 * usd, 2 * usd, "hold:hold_1"},
		{"extend", -usd, usd, 22 * usd, 3 * usd, "extend:ext_1"},
		{"settle", 0, -usd, 22 * usd, 2 * usd, "settle:ue_1"},
		// ue_2 settles nothing new; the final closes the hold.
		{"release", 2 * usd, -2 * usd, 24 * usd, 0, "release:hold_1"},
	}
	if len(es) != len(want) {
		t.Fatalf("%d entries: %+v", len(es), es)
	}
	for i, w := range want {
		e := es[i]
		if e.Kind != w.kind || e.AvailableDelta != w.da || e.HeldDelta != w.dh || e.AvailableAfter != w.after || e.HeldAfter != w.hel ||
			(w.key != "" && e.IdempotencyKey != w.key) {
			t.Errorf("entry %d: %+v, want %+v", i, e, w)
		}
	}
}

func TestCheckSubmitGatesOnStatusAndComputeEnabled(t *testing.T) {
	st := storetest.New(t)
	l := billingtest.New(t, st.Pool(), nil)
	db := st.Pool()
	billingtest.Account(t, db, 1)
	if err := l.CheckSubmit(ctx, 1); err != nil {
		t.Fatalf("active and enabled: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE accounts SET compute_enabled = false WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckSubmit(ctx, 1); !errors.Is(err, billing.ErrComputeNotEnabled) {
		t.Fatalf("compute off: %v", err)
	}
	for _, q := range []string{
		`UPDATE accounts SET status = 'suspended', suspended_reason = 'operator', compute_enabled = true WHERE id = 1`,
		`UPDATE accounts SET status = 'closed', suspended_reason = NULL, closed_at = now() WHERE id = 1`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
		if err := l.CheckSubmit(ctx, 1); !errors.Is(err, billing.ErrAccountSuspended) {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := l.CheckSubmit(ctx, 99); !errors.Is(err, billing.ErrUnknownAccount) {
		t.Fatalf("unknown: %v", err)
	}
}

// The ledger engine wired to the local biller directly, with no console and
// no HTTP in between: a job runs 25 minutes and finishes, and the account
// pays exactly quota(1500 s), in one hold, five settles and a release.
type noActions struct{}

func (noActions) Stop(context.Context, string, ledger.EndReason) error { return nil }
func (noActions) Notify(context.Context, string, string) error         { return nil }

func TestLedgerRunsOnTheLocalBillerDirectly(t *testing.T) {
	st := storetest.New(t)
	c := &clock{}
	c.Set(start)
	l := billingtest.New(t, st.Pool(), c.Now)
	billingtest.SetBalance(t, st.Pool(), l, user, 25*usd)
	t.Cleanup(func() { billingtest.AssertInvariant(t, l) })
	eng := ledger.New(st.Pool(), l, noActions{}, c.Now)

	const rate = "1.2000"
	job := store.NewJobID(c.Now())
	h, err := eng.PlaceHold(ctx, ledger.Job{JobID: job, UserID: user, TokenID: token, SKU: testSKU, RateUSDPerHour: rate,
		MaxRuntimeSeconds: 3600, SubmittedAt: c.Now()})
	if err != nil {
		t.Fatal(err)
	}
	upAt := c.Now().Add(time.Minute)
	c.Set(upAt.Unix())
	if err := eng.Started(ctx, job, 1, upAt); err != nil {
		t.Fatal(err)
	}
	for s := 15; s <= 1500; s += 15 {
		at := upAt.Add(time.Duration(s) * time.Second)
		c.Set(at.Unix())
		if err := eng.Up(ctx, job, 1, at); err != nil {
			t.Fatal(err)
		}
		if err := eng.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Stopped(ctx, job, 1, upAt.Add(1500*time.Second), ledger.EndExited); err != nil {
		t.Fatal(err)
	}
	if err := eng.Finished(ctx, job); err != nil {
		t.Fatal(err)
	}

	want, _ := sku.SKU{PriceUSDPerHour: rate}.EstimateQuota(1500)
	b, err := l.Balance(ctx, user)
	if err != nil || b.Available != 25*usd-want || b.Held != 0 {
		t.Fatalf("balance %+v %v, want available %d and nothing held", b, err, 25*usd-want)
	}
	rec, _, _ := l.HoldRecord(ctx, h.HoldID)
	// The last interval was settled before the end, so the ledger closes
	// with a release rather than a final settle of nothing.
	if rec.Status != billing.HoldReleased || rec.Settled != want {
		t.Fatalf("hold %+v", rec)
	}
	kinds := map[string]int{}
	es, _ := l.Entries(ctx, user)
	for _, e := range es {
		kinds[e.Kind]++
	}
	if kinds["hold"] != 1 || kinds["settle"] != 5 || kinds["release"] != 1 || kinds["extend"] != 0 {
		t.Fatalf("entries by kind %v", kinds)
	}

	// A hold the balance cannot cover is refused the way the console's 402 is.
	billingtest.SetBalance(t, st.Pool(), l, user, 1)
	_, err = eng.PlaceHold(ctx, ledger.Job{JobID: store.NewJobID(c.Now()), UserID: user, TokenID: token, SKU: testSKU,
		RateUSDPerHour: rate, MaxRuntimeSeconds: 3600, SubmittedAt: c.Now()})
	var refused *ledger.InsufficientBalanceError
	if !errors.As(err, &refused) {
		t.Fatalf("want InsufficientBalanceError, got %v", err)
	}
}

// A database error is not a bridge refusal: the ledger must see it as
// "unavailable" and retry, never as 402 or a closed hold.
func TestDatabaseFailureIsNotARefusal(t *testing.T) {
	st := storetest.New(t)
	l := billingtest.New(t, st.Pool(), nil)
	billingtest.Account(t, st.Pool(), user)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := l.Hold(cctx, holdReq("hold_1", 1))
	var ce *console.Error
	if err == nil || errors.As(err, &ce) {
		t.Fatalf("want a plain error, got %v", err)
	}
}
