package billing_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

// The hold contract of the console fake's own tests
// (internal/console/consoletest/fake_test.go), as one set of cases run
// against three billers with the same expected results: the console fake
// (in memory, over HTTP), the local biller called directly, as the ledger
// calls it under ACCOUNTS_BACKEND=local, and the local biller behind the
// fake's HTTP front. Cases that depend on console-only concepts (tenants,
// per-key spending limits, verify, the signature and the rate limiter) are
// left out; balances bind instead of key limits.

const (
	usd      = consoletest.QuotaPerUnit
	secret   = "0123456789abcdef0123456789abcdef"
	user     = 1042
	token    = 771
	start    = int64(1757520000)
	testSKU  = "a10g-24gb-x1"
	otherSKU = "h100-80gb-x1"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(unix int64) { c.mu.Lock(); defer c.mu.Unlock(); c.t = time.Unix(unix, 0) }

type verbs interface {
	Hold(ctx context.Context, req console.HoldRequest) (*console.HoldResult, error)
	Settle(ctx context.Context, holdID string, req console.SettleRequest) (*console.SettleResult, error)
	Extend(ctx context.Context, holdID string, req console.ExtendRequest) (*console.HoldResult, error)
	Release(ctx context.Context, holdID string) (*console.ReleaseResult, error)
}

type subject struct {
	verbs
	clock   *clock
	balance func() int64
	hold    func(id string) consoletest.Hold
	logs    func() []consoletest.ConsumeLog
	sweep   func()
}

func fakeSubject(t *testing.T, c *clock, book consoletest.Book) *subject {
	s := consoletest.New([]byte(secret))
	t.Cleanup(s.Close)
	s.SetNow(c.Now)
	if book != nil {
		s.UseBook(book)
	}
	s.SeedUser(consoletest.User{ID: user, Quota: 25 * usd})
	s.SeedKey(consoletest.Key{Key: "sk-test", TokenID: token, UserID: user, Unlimited: true})
	return &subject{verbs: s.Client(), clock: c, balance: func() int64 { return s.Balance(user) },
		hold: func(id string) consoletest.Hold { h, _ := s.Hold(id); return h }, logs: s.ConsumeLogs, sweep: s.Sweep}
}

func localSubject(t *testing.T) (*subject, *billing.Local) {
	c := &clock{}
	c.Set(start)
	st := storetest.New(t)
	book, l := billingtest.Book(t, st.Pool(), c.Now)
	book.SetBalance(user, 25*usd)
	t.Cleanup(func() { billingtest.AssertInvariant(t, l) })
	return &subject{verbs: l, clock: c, balance: func() int64 { return book.Balance(user) },
		hold: func(id string) consoletest.Hold { h, _ := book.Inspect(id); return h }, logs: book.ConsumeLogs,
		sweep: func() {
			if err := book.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
		}}, l
}

// each runs fn against every biller.
func each(t *testing.T, fn func(t *testing.T, s *subject)) {
	t.Run("console fake", func(t *testing.T) {
		t.Parallel()
		c := &clock{}
		c.Set(start)
		fn(t, fakeSubject(t, c, nil))
	})
	t.Run("local", func(t *testing.T) {
		t.Parallel()
		s, _ := localSubject(t)
		fn(t, s)
	})
	t.Run("local over HTTP", func(t *testing.T) {
		t.Parallel()
		c := &clock{}
		c.Set(start)
		st := storetest.New(t)
		book, l := billingtest.Book(t, st.Pool(), c.Now)
		t.Cleanup(func() { billingtest.AssertInvariant(t, l) })
		// One clock for the fake's signature window and the book's expiry.
		fn(t, fakeSubject(t, c, book))
	})
}

func refusal(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	var e *console.Error
	if !errors.As(err, &e) || e.Status != wantStatus || e.Message != wantCode {
		t.Errorf("want %d %s, got %v", wantStatus, wantCode, err)
	}
}

func holdReq(id string, q int64) console.HoldRequest {
	return console.HoldRequest{HoldID: id, UserID: user, TokenID: token, Quota: q, JobID: "job_" + id, SKU: testSKU, ExpiresAt: start + 3600}
}

func ext(id string, q int64) console.ExtendRequest {
	return console.ExtendRequest{ExtendID: id, AdditionalQuota: q}
}

func settle(id string, cum int64) console.SettleRequest {
	return console.SettleRequest{EventID: id, CumulativeQuota: cum, SKU: testSKU}
}

var ctx = context.Background()

func TestContractHoldSettleReleaseMovesQuota(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		h, err := s.Hold(ctx, holdReq("hold_1", 1_270_000))
		if err != nil {
			t.Fatal(err)
		}
		if h.RemainQuota != 25*usd-1_270_000 || h.Quota != 1_270_000 || h.HoldID != "hold_1" {
			t.Fatalf("hold %+v", h)
		}
		ev := settle("ue_1", 430_000)
		ev.GPUSeconds = 1290
		st, err := s.Settle(ctx, "hold_1", ev)
		if err != nil || st.SettledQuota != 430_000 || st.HoldRemaining != 840_000 {
			t.Fatalf("settle %+v %v", st, err)
		}
		ev = settle("ue_2", 500_000)
		ev.GPUSeconds = 1500
		if _, err := s.Settle(ctx, "hold_1", ev); err != nil {
			t.Fatal(err)
		}
		r, err := s.Release(ctx, "hold_1")
		if err != nil || r.ReleasedQuota != 770_000 || r.RemainQuota != 25*usd-500_000 {
			t.Fatalf("release %+v %v", r, err)
		}
		again, err := s.Release(ctx, "hold_1")
		if err != nil || *again != *r {
			t.Fatalf("release is idempotent: %+v %v", again, err)
		}
		if s.balance() != 25*usd-500_000 {
			t.Fatalf("second release moved quota: %d", s.balance())
		}
		logs := s.logs()
		if len(logs) != 2 || logs[0].Quota != 430_000 || logs[1].Quota != 70_000 || logs[0].ModelName != "compute/"+testSKU ||
			logs[1].GPUSeconds != 1500 || logs[0].JobID != "job_hold_1" || logs[0].EventID != "ue_1" || logs[0].UserID != user {
			t.Fatalf("consume logs %+v", logs)
		}
		if hd := s.hold("hold_1"); hd.Status != consoletest.HoldReleased || hd.Settled != 500_000 || hd.Released != 770_000 {
			t.Fatalf("hold %+v", hd)
		}
		_, err = s.Settle(ctx, "hold_1", settle("ue_3", 600_000))
		refusal(t, err, 409, consoletest.CodeHoldClosed)
	})
}

func TestContractHoldIsIdempotentAndRefusesWithoutBalance(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		first, err := s.Hold(ctx, holdReq("hold_1", usd))
		if err != nil {
			t.Fatal(err)
		}
		replay, err := s.Hold(ctx, holdReq("hold_1", usd))
		if err != nil || *replay != *first {
			t.Fatalf("replay %+v %v, want %+v", replay, err, first)
		}
		if s.balance() != 24*usd {
			t.Fatalf("replay debited twice: %d", s.balance())
		}
		for name, req := range map[string]console.HoldRequest{
			"quota":      holdReq("hold_1", usd+1),
			"sku":        func() console.HoldRequest { r := holdReq("hold_1", usd); r.SKU = otherSKU; return r }(),
			"expires_at": func() console.HoldRequest { r := holdReq("hold_1", usd); r.ExpiresAt++; return r }(),
			"job_id":     func() console.HoldRequest { r := holdReq("hold_1", usd); r.JobID = "job_other"; return r }(),
		} {
			_, err := s.Hold(ctx, req)
			t.Run(name, func(t *testing.T) { refusal(t, err, 409, consoletest.CodeIdempotencyConflict) })
		}
		if s.balance() != 24*usd {
			t.Fatalf("a conflicting replay moved quota: %d", s.balance())
		}
		_, err = s.Hold(ctx, holdReq("hold_3", 24*usd+1))
		refusal(t, err, 402, consoletest.CodeInsufficientBalance)
		if _, err := s.Hold(ctx, holdReq("hold_4", 24*usd)); err != nil {
			t.Fatalf("exact balance should hold: %v", err)
		}
		if s.balance() != 0 {
			t.Fatalf("balance %d", s.balance())
		}
		_, err = s.Hold(ctx, console.HoldRequest{HoldID: "hold_5", UserID: user, TokenID: token})
		refusal(t, err, 400, consoletest.CodeInvalidRequest)
		_, err = s.Hold(ctx, console.HoldRequest{UserID: user, TokenID: token, Quota: 1})
		refusal(t, err, 400, consoletest.CodeInvalidRequest)
		unknown := holdReq("hold_6", 1)
		unknown.UserID = 9999
		_, err = s.Hold(ctx, unknown)
		refusal(t, err, 400, consoletest.CodeInvalidRequest)
	})
}

func TestContractSettleIsIdempotentBoundedAndFinal(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		if _, err := s.Hold(ctx, holdReq("hold_1", 1_000_000)); err != nil {
			t.Fatal(err)
		}
		ev := settle("ue_1", 400_000)
		a, _ := s.Settle(ctx, "hold_1", ev)
		b, err := s.Settle(ctx, "hold_1", ev)
		if err != nil || *a != *b || len(s.logs()) != 1 {
			t.Fatalf("replayed event charged again: %+v %+v logs=%d", a, b, len(s.logs()))
		}
		_, err = s.Settle(ctx, "hold_1", settle("ue_2", 1_000_001))
		refusal(t, err, 409, consoletest.CodeHoldExceeded)
		_, err = s.Settle(ctx, "hold_1", settle("ue_2b", 399_999))
		refusal(t, err, 409, consoletest.CodeCumulativeQuotaRegressed)
		_, err = s.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2c", CumulativeQuota: 500_000, SKU: otherSKU})
		refusal(t, err, 409, consoletest.CodeSKUMismatch)
		_, err = s.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2e", CumulativeQuota: 500_000})
		refusal(t, err, 400, consoletest.CodeInvalidRequest)
		_, err = s.Settle(ctx, "hold_1", console.SettleRequest{CumulativeQuota: 500_000, SKU: testSKU})
		refusal(t, err, 400, consoletest.CodeInvalidRequest)
		if len(s.logs()) != 1 {
			t.Fatalf("refused settles wrote consume logs: %+v", s.logs())
		}
		if r, err := s.Settle(ctx, "hold_1", settle("ue_2d", 400_000)); err != nil || r.SettledQuota != 400_000 {
			t.Fatalf("an equal cumulative is not a regression: %+v %v", r, err)
		}
		if _, err := s.Extend(ctx, "hold_1", ext("ext_1", 100_000)); err != nil {
			t.Fatal(err)
		}
		final := settle("ue_3", 1_000_001)
		final.Final = true
		f, err := s.Settle(ctx, "hold_1", final)
		if err != nil || f.SettledQuota != 1_000_001 || f.HoldRemaining != 0 {
			t.Fatalf("final %+v %v", f, err)
		}
		if s.balance() != 25*usd-1_000_001 {
			t.Fatalf("final did not release the remainder: %d", s.balance())
		}
		if h := s.hold("hold_1"); h.Status != consoletest.HoldSettled || h.Released != 99_999 {
			t.Fatalf("hold %+v", h)
		}
		if r, err := s.Release(ctx, "hold_1"); err != nil || r.ReleasedQuota != 99_999 {
			t.Fatalf("release after final: %+v %v", r, err)
		}
		_, err = s.Settle(ctx, "hold_1", settle("ue_5", 1_000_001))
		refusal(t, err, 409, consoletest.CodeHoldClosed)
		_, err = s.Settle(ctx, "nope", settle("ue_4", 0))
		if !errors.Is(err, console.ErrHoldNotFound) || errors.Is(err, console.ErrBridgeDisabled) {
			t.Fatalf("unknown hold: want ErrHoldNotFound, got %v", err)
		}
		_, err = s.Settle(ctx, "nope", console.SettleRequest{EventID: "ue_6"})
		refusal(t, err, 404, consoletest.CodeHoldNotFound)
		_, err = s.Settle(ctx, "nope", ev)
		refusal(t, err, 409, consoletest.CodeIdempotencyConflict)
	})
}

func TestContractExtend(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		if _, err := s.Hold(ctx, holdReq("hold_1", usd)); err != nil {
			t.Fatal(err)
		}
		out, err := s.Extend(ctx, "hold_1", ext("ext_01J8A", 300_000))
		if err != nil || out.HoldID != "hold_1" || out.Quota != 800_000 || out.RemainQuota != 25*usd-800_000 {
			t.Fatalf("extend %+v %v", out, err)
		}
		if r, err := s.Hold(ctx, holdReq("hold_1", usd)); err != nil || r.Quota != usd {
			t.Fatalf("hold replay after extend: %+v %v", r, err)
		}
		_, err = s.Hold(ctx, holdReq("hold_1", 800_000))
		refusal(t, err, 409, consoletest.CodeIdempotencyConflict)
		_, err = s.Extend(ctx, "hold_1", ext("ext_01J8B", 25*usd-800_000+1))
		refusal(t, err, 402, consoletest.CodeInsufficientBalance)
		if h := s.hold("hold_1"); h.Quota != 800_000 {
			t.Fatalf("refused extend changed the hold: %+v", h)
		}
		for name, req := range map[string]console.ExtendRequest{
			"no extend_id":    ext("", 1),
			"extend_id > 64":  ext("ext_"+strings.Repeat("A", 61), 1),
			"no amount":       ext("ext_01J8C", 0),
			"negative amount": ext("ext_01J8D", -1),
		} {
			_, err := s.Extend(ctx, "hold_1", req)
			t.Run(name, func(t *testing.T) { refusal(t, err, 400, consoletest.CodeInvalidRequest) })
		}
		if _, err := s.Extend(ctx, "hold_1", ext("ext_"+strings.Repeat("A", 60), 1)); err != nil {
			t.Errorf("64-char extend_id: %v", err)
		}
		if _, err := s.Extend(ctx, "nope", ext("ext_01J8E", 1)); !errors.Is(err, console.ErrHoldNotFound) {
			t.Errorf("unknown hold: %v", err)
		}
		if _, err := s.Extend(ctx, "nope", ext("ext_01J8A", 300_000)); err == nil {
			t.Error("an extend_id used on another hold must not succeed on an unknown one")
		} else {
			refusal(t, err, 409, consoletest.CodeIdempotencyConflict)
		}
	})
}

func TestContractExtendIsIdempotentOnExtendID(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		for _, id := range []string{"hold_1", "hold_2"} {
			if _, err := s.Hold(ctx, holdReq(id, usd)); err != nil {
				t.Fatal(err)
			}
		}
		before := s.balance()
		out, err := s.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000))
		if err != nil || out.Quota != 800_000 || out.RemainQuota != before-300_000 {
			t.Fatalf("extend: %+v %v", out, err)
		}
		again, err := s.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000))
		if err != nil || *again != *out {
			t.Fatalf("retry: %+v %v", again, err)
		}
		if s.balance() != before-300_000 {
			t.Fatalf("reserved more than once: %d", s.balance())
		}
		_, err = s.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_001))
		refusal(t, err, 409, consoletest.CodeIdempotencyConflict)
		_, err = s.Extend(ctx, "hold_2", ext("ext_01J8RETRY", 300_000))
		refusal(t, err, 409, consoletest.CodeIdempotencyConflict)
		if s.balance() != before-300_000 {
			t.Fatalf("a conflicting extend moved quota: %d", s.balance())
		}
		if _, err := s.Extend(ctx, "hold_1", ext("ext_01J8MORE", 100_000)); err != nil {
			t.Fatal(err)
		}
		if r, err := s.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000)); err != nil || r.Quota != 900_000 || r.RemainQuota != before-400_000 {
			t.Fatalf("replay reports current state: %+v %v", r, err)
		}
		if _, err := s.Release(ctx, "hold_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000)); err != nil {
			t.Fatalf("replay after release: %v", err)
		}
		_, err = s.Extend(ctx, "hold_1", ext("ext_01J8NEW", 1))
		refusal(t, err, 409, consoletest.CodeHoldClosed)
	})
}

func TestContractExpiredHoldsAreSweptAndReported(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		if _, err := s.Hold(ctx, holdReq("hold_1", usd)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Settle(ctx, "hold_1", settle("ue_1", 100_000)); err != nil {
			t.Fatal(err)
		}
		s.clock.Set(start + 3600)
		s.sweep()
		if h := s.hold("hold_1"); h.Status != consoletest.HoldActive || s.balance() != 24*usd {
			t.Fatalf("expired at expires_at itself, not after it: %+v balance %d", h, s.balance())
		}
		s.clock.Set(start + 3601)
		s.sweep()
		if s.balance() != 25*usd-100_000 {
			t.Fatalf("sweep did not return the unsettled part: %d", s.balance())
		}
		if h := s.hold("hold_1"); h.Status != consoletest.HoldExpired || h.Released != usd-100_000 {
			t.Fatalf("hold %+v", h)
		}
		_, err := s.Settle(ctx, "hold_1", settle("ue_2", 200_000))
		refusal(t, err, 409, consoletest.CodeHoldExpired)
		_, err = s.Release(ctx, "hold_1")
		refusal(t, err, 409, consoletest.CodeHoldExpired)
		_, err = s.Extend(ctx, "hold_1", ext("ext_1", 1))
		refusal(t, err, 409, consoletest.CodeHoldExpired)
		if r, err := s.Settle(ctx, "hold_1", settle("ue_1", 100_000)); err != nil || r.SettledQuota != 100_000 {
			t.Errorf("an event settled before expiry still replays: %+v %v", r, err)
		}
	})
}

// Expiry does not wait for the sweep: the next call that touches the
// account sees it, and the remainder is spendable at once.
func TestContractExpiryIsSeenWithoutASweep(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		if _, err := s.Hold(ctx, holdReq("hold_1", 25*usd)); err != nil {
			t.Fatal(err)
		}
		s.clock.Set(start + 3601)
		next := holdReq("hold_2", 25*usd)
		next.ExpiresAt = start + 7200
		if _, err := s.Hold(ctx, next); err != nil {
			t.Fatalf("the expired hold's quota should be spendable: %v", err)
		}
		_, err := s.Release(ctx, "hold_1")
		refusal(t, err, 409, consoletest.CodeHoldExpired)
	})
}

// No hold without an expires_at ever expires.
func TestContractZeroExpiryNeverExpires(t *testing.T) {
	each(t, func(t *testing.T, s *subject) {
		req := holdReq("hold_1", usd)
		req.ExpiresAt = 0
		if _, err := s.Hold(ctx, req); err != nil {
			t.Fatal(err)
		}
		s.clock.Set(start + 10*365*86400)
		s.sweep()
		if h := s.hold("hold_1"); h.Status != consoletest.HoldActive {
			t.Fatalf("hold %+v", h)
		}
	})
}
