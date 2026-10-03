package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const (
	testRate    = "1.2000" // A10G placeholder: quota(300) = 50000, quota(3600) = 600000
	testSKU     = "a10g-24gb-x1"
	testUser    = 1042
	testToken   = 771
	testBalance = 100_000_000
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type stopCall struct {
	JobID  string
	Reason EndReason
	At     time.Time
}

type recorder struct {
	clock   *fakeClock
	mu      sync.Mutex
	stops   []stopCall
	notes   []string
	failing bool
}

func (r *recorder) Stop(_ context.Context, jobID string, reason EndReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failing {
		return errors.New("backend unavailable")
	}
	r.stops = append(r.stops, stopCall{jobID, reason, r.clock.Now()})
	return nil
}

func (r *recorder) Notify(_ context.Context, jobID, event string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, jobID+":"+event)
	return nil
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	db    *pgxpool.Pool
	fake  *consoletest.Server
	clock *fakeClock
	act   *recorder
	eng   *Engine
}

// newHarness gives each test its own schema, a fake console sharing a fake
// clock, and an engine. It skips without COMPUTE_TEST_DATABASE_URL, like the
// store's tests.
func newHarness(t *testing.T) *harness {
	t.Helper()
	url := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	t.Parallel()
	ctx := context.Background()

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "ledger_test_" + hex.EncodeToString(suffix[:])
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	scoped := url + sep + "search_path=" + schema

	st, err := store.Open(ctx, scoped)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st.Close()
	db, err := pgxpool.New(ctx, scoped)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(db.Close)

	clock := &fakeClock{t: t0}
	fake := consoletest.New([]byte("0123456789abcdef0123456789abcdef"))
	t.Cleanup(fake.Close)
	fake.SetNow(clock.Now)
	if billingtest.Parity(t) {
		book, local := billingtest.Book(t, db, clock.Now)
		fake.UseBook(book)
		t.Cleanup(func() { billingtest.AssertInvariant(t, local) })
	}
	fake.SeedUser(consoletest.User{ID: testUser, Quota: testBalance})
	fake.SeedKey(consoletest.Key{Key: "sk-test", TokenID: testToken, UserID: testUser, Unlimited: true})

	act := &recorder{clock: clock}
	return &harness{t: t, ctx: ctx, db: db, fake: fake, clock: clock, act: act,
		eng: New(db, fake.Client(), act, clock.Now)}
}

func (h *harness) at(sec float64) time.Time {
	return t0.Add(time.Duration(sec * float64(time.Second)))
}

func (h *harness) setBalance(q int64) {
	h.fake.SeedUser(consoletest.User{ID: testUser, Quota: q})
}

func (h *harness) placeHold(maxRuntime int64) string {
	h.t.Helper()
	id := store.NewJobID(h.clock.Now())
	if _, err := h.eng.PlaceHold(h.ctx, h.job(id, maxRuntime)); err != nil {
		h.t.Fatalf("place hold: %v", err)
	}
	return id
}

func (h *harness) job(id string, maxRuntime int64) Job {
	return Job{JobID: id, UserID: testUser, TokenID: testToken, SKU: testSKU, RateUSDPerHour: testRate,
		MaxRuntimeSeconds: maxRuntime, SubmittedAt: h.clock.Now()}
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) tick() {
	h.t.Helper()
	h.must(h.eng.Tick(h.ctx))
}

// run reports the attempt UP every `every` seconds from the clock's current
// time until `until` (seconds after t0), ticking after each observation.
func (h *harness) run(jobID string, attempt int, until, every float64) {
	h.t.Helper()
	for s := h.clock.Now().Sub(t0).Seconds() + every; s <= until+1e-9; s += every {
		h.clock.Set(h.at(s))
		h.must(h.eng.Up(h.ctx, jobID, attempt, h.at(s)))
		h.tick()
	}
}

// idle advances the clock without UP observations (provisioning, a retry's
// provisioning, or a job between attempts), ticking along the way.
func (h *harness) idle(until, every float64) {
	h.t.Helper()
	for s := h.clock.Now().Sub(t0).Seconds() + every; s <= until+1e-9; s += every {
		h.clock.Set(h.at(s))
		h.tick()
	}
}

func (h *harness) start(jobID string, attempt int, sec float64) {
	h.t.Helper()
	h.clock.Set(h.at(sec))
	h.must(h.eng.Started(h.ctx, jobID, attempt, h.at(sec)))
	h.tick()
}

func (h *harness) finish(jobID string) {
	h.t.Helper()
	h.must(h.eng.Finished(h.ctx, jobID))
}

func (h *harness) hold(jobID string) Hold {
	h.t.Helper()
	hd, err := h.eng.Hold(h.ctx, jobID)
	h.must(err)
	return hd
}

func (h *harness) events(jobID string) []UsageEvent {
	h.t.Helper()
	evs, err := h.eng.Events(h.ctx, jobID)
	h.must(err)
	return evs
}

// charged is what the console's consume log says this job cost.
func (h *harness) charged(jobID string) int64 {
	var sum int64
	for _, l := range h.fake.ConsumeLogs() {
		if l.JobID == jobID {
			sum += l.Quota
		}
	}
	return sum
}

func (h *harness) consumeRows(jobID string) int {
	n := 0
	for _, l := range h.fake.ConsumeLogs() {
		if l.JobID == jobID {
			n++
		}
	}
	return n
}

type settleBody struct {
	EventID         string `json:"event_id"`
	CumulativeQuota int64  `json:"cumulative_quota"`
	GPUSeconds      int64  `json:"gpu_seconds"`
	Final           bool   `json:"final"`
}

func (h *harness) settles() []settleBody {
	var out []settleBody
	for _, c := range h.fake.CallsTo(consoletest.RouteSettle) {
		var b settleBody
		h.must(json.Unmarshal(c.Body, &b))
		out = append(out, b)
	}
	return out
}

type extendBody struct {
	ExtendID        string `json:"extend_id"`
	AdditionalQuota int64  `json:"additional_quota"`
}

func (h *harness) extends() []extendBody {
	var out []extendBody
	for _, c := range h.fake.CallsTo(consoletest.RouteExtend) {
		var b extendBody
		h.must(json.Unmarshal(c.Body, &b))
		out = append(out, b)
	}
	return out
}

// assertSettledExactly checks the end state every terminal job must reach:
// the console charged exactly quota(seconds), the hold is closed, and the
// balance moved by the charge and nothing else.
func (h *harness) assertSettledExactly(jobID string, seconds int64) {
	h.t.Helper()
	want := quota(testRate, seconds)
	if got := h.charged(jobID); got != want {
		h.t.Errorf("console charged %d, want quota(%d s) = %d", got, seconds, want)
	}
	evs := h.events(jobID)
	var qty, amt int64
	for _, ev := range evs {
		qty += ev.Quantity
		amt += ev.Amount
	}
	if qty != seconds || amt != want {
		h.t.Errorf("ledger sums to %d s / %d quota, want %d s / %d", qty, amt, seconds, want)
	}
	hd := h.hold(jobID)
	if hd.State != "closed" {
		h.t.Errorf("hold state %q, want closed", hd.State)
	}
	ch, _ := h.fake.Hold(hd.HoldID)
	if ch.Status == consoletest.HoldActive {
		h.t.Errorf("console hold still active")
	}
	if ch.Settled != want {
		h.t.Errorf("console hold settled %d, want %d", ch.Settled, want)
	}
	if b := h.fake.Balance(testUser); b != testBalance-want {
		h.t.Errorf("balance %d, want %d (initial − charge)", b, testBalance-want)
	}
}
