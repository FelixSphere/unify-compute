package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
)

// newTestStore gives each test a migrated Store in a schema of its own, so
// tests run in parallel against one database and leave nothing behind. It
// skips unless COMPUTE_TEST_DATABASE_URL names a Postgres the test may write.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "store_test_" + hex.EncodeToString(suffix[:])

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

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	s, err := openConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func hashOf(body string) []byte {
	h := sha256.Sum256([]byte(body))
	return h[:]
}

func newJob(owner string) NewJob {
	return NewJob{
		Owner:    owner,
		SKU:      "a10g-24gb-x1",
		Spec:     json.RawMessage(`{"image":"ghcr.io/acme/embed:1.4","max_runtime_seconds":7200}`),
		Metadata: map[string]string{"pipeline": "nightly-embed"},
	}
}

func mustCreate(t *testing.T, s *Store, nj NewJob) Job {
	t.Helper()
	j, created, err := s.Create(context.Background(), nj, nil)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	return j
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for range 3 {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("re-migrate: %v", err)
		}
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(ms) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(ms))
	}
}

func TestMigrateConcurrent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.pool.Exec(context.Background(), `DROP TABLE idempotency_keys, jobs, schema_migrations`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Migrate(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
	mustCreate(t, s, newJob("user:1"))
}

func TestMigrateRefusesEditedMigration(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE schema_migrations SET checksum = '\x00' WHERE version = '0001_jobs'`); err != nil {
		t.Fatal(err)
	}
	err := s.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "different checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
}

func TestCreateAndGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	token := int64(771)
	nj := newJob("user:1042")
	nj.TokenID = &token

	j := mustCreate(t, s, nj)
	if !jobIDPattern.MatchString(j.ID) {
		t.Fatalf("id %q", j.ID)
	}
	if j.Status != StatusQueued || j.StartedAt != nil || j.FinishedAt != nil || j.Error != nil || j.Placement != nil {
		t.Fatalf("new job not plainly queued: %+v", j)
	}

	got, err := s.Get(ctx, "user:1042", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SKU != nj.SKU || got.Metadata["pipeline"] != "nightly-embed" || got.TokenID == nil || *got.TokenID != 771 {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	var spec map[string]any
	if err := json.Unmarshal(got.Spec, &spec); err != nil || spec["image"] != "ghcr.io/acme/embed:1.4" {
		t.Fatalf("spec round trip: %s (%v)", got.Spec, err)
	}
	if !got.CreatedAt.Equal(j.CreatedAt) {
		t.Fatalf("created_at %v != %v", got.CreatedAt, j.CreatedAt)
	}
}

func TestCreateWithoutMetadataStoresEmptyObject(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	nj := newJob("user:1")
	nj.Metadata = nil
	j := mustCreate(t, s, nj)
	if j.Metadata == nil || len(j.Metadata) != 0 {
		t.Fatalf("metadata = %#v, want empty map", j.Metadata)
	}
}

func TestCreateRejectsNonObjectSpec(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	nj := newJob("user:1")
	nj.Spec = json.RawMessage(`[1,2]`)
	if _, _, err := s.Create(context.Background(), nj, nil); err == nil {
		t.Fatal("array spec was accepted")
	}
}

func TestGetIsOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	j := mustCreate(t, s, newJob("user:1"))
	ctx := context.Background()

	_, errOther := s.Get(ctx, "user:2", j.ID)
	_, errMissing := s.Get(ctx, "user:2", NewJobID(time.Now()))
	if !errors.Is(errOther, ErrNotFound) || !errors.Is(errMissing, ErrNotFound) {
		t.Fatalf("other owner: %v, missing: %v; both must be ErrNotFound", errOther, errMissing)
	}
	if errOther.Error() != errMissing.Error() {
		t.Fatalf("errors differ, which leaks existence: %q vs %q", errOther, errMissing)
	}
}

func TestIdempotentReplay(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	idem := &Idempotency{Key: "submit-1", RequestHash: hashOf(`{"sku":"a"}`)}

	first, created, err := s.Create(ctx, newJob("user:1"), idem)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	again, created, err := s.Create(ctx, newJob("user:1"), idem)
	if err != nil || created {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	if again.ID != first.ID {
		t.Fatalf("replay returned %s, want %s", again.ID, first.ID)
	}
	var n int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d jobs persisted, want 1 (the replay's insert must roll back)", n)
	}
}

func TestIdempotentReplayReturnsCurrentState(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	idem := &Idempotency{Key: "k", RequestHash: hashOf("b")}
	first, _, err := s.Create(ctx, newJob("user:1"), idem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStatus(ctx, first.ID, StatusProvisioning, nil); err != nil {
		t.Fatal(err)
	}
	again, _, err := s.Create(ctx, newJob("user:1"), idem)
	if err != nil || again.Status != StatusProvisioning {
		t.Fatalf("replay status %q err %v, want provisioning", again.Status, err)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if _, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("a")}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("b")})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestIdempotencyKeysAreOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	idem := &Idempotency{Key: "shared-key", RequestHash: hashOf("a")}
	a, _, err := s.Create(ctx, newJob("user:1"), idem)
	if err != nil {
		t.Fatal(err)
	}
	b, created, err := s.Create(ctx, newJob("user:2"), &Idempotency{Key: "shared-key", RequestHash: hashOf("different")})
	if err != nil || !created || b.ID == a.ID {
		t.Fatalf("second owner's key collided with the first's: created=%v err=%v", created, err)
	}
}

func TestIdempotencyKeyLength(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, key := range []string{"", strings.Repeat("k", 256), strings.Repeat("é", 256)} {
		if _, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: key, RequestHash: hashOf("a")}); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Fatalf("key of %d runes: want ErrInvalidIdempotencyKey, got %v", len([]rune(key)), err)
		}
	}
	for _, key := range []string{strings.Repeat("k", 255), strings.Repeat("é", 255)} {
		if _, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: key, RequestHash: hashOf("a")}); err != nil {
			t.Fatalf("key of 255 runes refused: %v", err)
		}
	}
}

func TestExpiredIdempotencyKeyIsReusable(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	first, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE idempotency_keys SET created_at = now() - interval '24 hours 1 second'`); err != nil {
		t.Fatal(err)
	}
	second, created, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("b")})
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("expired key was not reclaimed: created=%v err=%v", created, err)
	}
	replay, created, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("b")})
	if err != nil || created || replay.ID != second.ID {
		t.Fatalf("reclaimed key does not replay the new job: created=%v err=%v", created, err)
	}
}

func TestConcurrentSubmitsWithOneKeyMakeOneJob(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	const n = 16
	ids := make(chan string, n)
	var createdCount int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, created, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: "race", RequestHash: hashOf("a")})
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			if created {
				mu.Lock()
				createdCount++
				mu.Unlock()
			}
			ids <- j.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent submits returned different jobs: %s and %s", first, id)
		}
	}
	if createdCount != 1 {
		t.Fatalf("%d submits reported created, want 1", createdCount)
	}
	var count int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&count)
	if count != 1 {
		t.Fatalf("%d jobs persisted, want 1", count)
	}
}

func TestPurgeExpiredIdempotencyKeys(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, k := range []string{"old", "fresh"} {
		if _, _, err := s.Create(ctx, newJob("user:1"), &Idempotency{Key: k, RequestHash: hashOf(k)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE idempotency_keys SET created_at = now() - interval '25 hours' WHERE key = 'old'`); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeExpiredIdempotencyKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged %d (err %v), want 1", n, err)
	}
	var left []string
	rows, _ := s.pool.Query(ctx, `SELECT key FROM idempotency_keys`)
	left, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(left) != 1 || left[0] != "fresh" {
		t.Fatalf("remaining keys %v (err %v), want [fresh]", left, err)
	}
	var jobs int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&jobs)
	if jobs != 2 {
		t.Fatalf("purge removed jobs: %d left, want 2", jobs)
	}
}

func TestListNewestFirstWithCursor(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	var created []string
	for range 7 {
		created = append(created, mustCreate(t, s, newJob("user:1")).ID)
	}
	mustCreate(t, s, newJob("user:2"))

	var got []string
	after := ""
	pages := 0
	for {
		p, err := s.List(ctx, ListParams{Owner: "user:1", Limit: 3, After: after})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, j := range p.Jobs {
			got = append(got, j.ID)
		}
		if !p.HasMore {
			if p.Next != "" {
				t.Fatalf("last page has Next %q", p.Next)
			}
			break
		}
		if len(p.Jobs) != 3 || p.Next != p.Jobs[2].ID {
			t.Fatalf("full page: %d jobs, next %q", len(p.Jobs), p.Next)
		}
		after = p.Next
	}
	if pages != 3 {
		t.Fatalf("%d pages, want 3", pages)
	}
	if len(got) != len(created) {
		t.Fatalf("listed %d jobs, want %d: %v", len(got), len(created), got)
	}
	for i := range got {
		if got[i] != created[len(created)-1-i] {
			t.Fatalf("order: got %v, want reverse of %v", got, created)
		}
	}
}

func TestListExactPageHasNoMore(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	for range 3 {
		mustCreate(t, s, newJob("user:1"))
	}
	p, err := s.List(context.Background(), ListParams{Owner: "user:1", Limit: 3})
	if err != nil || len(p.Jobs) != 3 || p.HasMore {
		t.Fatalf("jobs=%d has_more=%v err=%v", len(p.Jobs), p.HasMore, err)
	}
}

func TestListStatusFilter(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	queued := mustCreate(t, s, newJob("user:1"))
	prov := mustCreate(t, s, newJob("user:1"))
	running := mustCreate(t, s, newJob("user:1"))
	if _, err := s.UpdateStatus(ctx, prov.ID, StatusProvisioning, nil); err != nil {
		t.Fatal(err)
	}
	for _, to := range []Status{StatusProvisioning, StatusRunning} {
		if _, err := s.UpdateStatus(ctx, running.ID, to, nil); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(p Page) []string {
		var out []string
		for _, j := range p.Jobs {
			out = append(out, j.ID)
		}
		return out
	}
	p, err := s.List(ctx, ListParams{Owner: "user:1", Statuses: []Status{StatusRunning}})
	if err != nil || len(p.Jobs) != 1 || p.Jobs[0].ID != running.ID {
		t.Fatalf("running filter: %v (err %v)", ids(p), err)
	}
	p, err = s.List(ctx, ListParams{Owner: "user:1", Statuses: []Status{StatusQueued, StatusRunning}})
	if err != nil || len(p.Jobs) != 2 || p.Jobs[0].ID != running.ID || p.Jobs[1].ID != queued.ID {
		t.Fatalf("queued+running filter: %v (err %v)", ids(p), err)
	}
	if _, err := s.List(ctx, ListParams{Owner: "user:1", Statuses: []Status{"bogus"}}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("bogus status: want ErrInvalidStatus, got %v", err)
	}
}

func TestListCursorIsOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	other := mustCreate(t, s, newJob("user:2"))
	mustCreate(t, s, newJob("user:1"))
	for _, after := range []string{other.ID, NewJobID(time.Now())} {
		if _, err := s.List(ctx, ListParams{Owner: "user:1", After: after}); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %s: want ErrInvalidCursor, got %v", after, err)
		}
	}
}

func TestListLimitBounds(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for range MaxListLimit + 1 {
		mustCreate(t, s, newJob("user:1"))
	}
	p, err := s.List(ctx, ListParams{Owner: "user:1"})
	if err != nil || len(p.Jobs) != DefaultListLimit || !p.HasMore {
		t.Fatalf("default limit: %d jobs, has_more=%v, err=%v", len(p.Jobs), p.HasMore, err)
	}
	p, err = s.List(ctx, ListParams{Owner: "user:1", Limit: 1000})
	if err != nil || len(p.Jobs) != MaxListLimit || !p.HasMore {
		t.Fatalf("capped limit: %d jobs, has_more=%v, err=%v", len(p.Jobs), p.HasMore, err)
	}
}

var allStatuses = []Status{
	StatusQueued, StatusProvisioning, StatusRunning,
	StatusSucceeded, StatusFailed, StatusInterrupted, StatusCancelled,
}

// pathTo is the shortest allowed route from queued to each status, used to
// put a job into a given state before testing transitions out of it.
var pathTo = map[Status][]Status{
	StatusQueued:       nil,
	StatusProvisioning: {StatusProvisioning},
	StatusRunning:      {StatusProvisioning, StatusRunning},
	StatusSucceeded:    {StatusProvisioning, StatusRunning, StatusSucceeded},
	StatusFailed:       {StatusProvisioning, StatusRunning, StatusFailed},
	StatusInterrupted:  {StatusProvisioning, StatusRunning, StatusInterrupted},
	StatusCancelled:    {StatusCancelled},
}

// The diagram in docs/spec/job-api.md, written out independently of the
// transitions table so a change to one without the other fails here.
var wantAllowed = map[[2]Status]bool{
	{StatusQueued, StatusProvisioning}:    true,
	{StatusQueued, StatusCancelled}:       true,
	{StatusProvisioning, StatusRunning}:   true,
	{StatusProvisioning, StatusCancelled}: true,
	{StatusRunning, StatusSucceeded}:      true,
	{StatusRunning, StatusFailed}:         true,
	{StatusRunning, StatusInterrupted}:    true,
	{StatusRunning, StatusCancelled}:      true,
}

func TestUpdateStatusEnforcesStateDiagram(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			if CanTransition(from, to) != wantAllowed[[2]Status{from, to}] {
				t.Errorf("CanTransition(%s, %s) = %v", from, to, !wantAllowed[[2]Status{from, to}])
			}
			j := mustCreate(t, s, newJob("user:1"))
			for _, step := range pathTo[from] {
				if _, err := s.UpdateStatus(ctx, j.ID, step, nil); err != nil {
					t.Fatalf("setup %s -> %s: %v", j.Status, step, err)
				}
			}
			got, err := s.UpdateStatus(ctx, j.ID, to, nil)
			if wantAllowed[[2]Status{from, to}] {
				if err != nil || got.Status != to {
					t.Errorf("%s -> %s refused: %v", from, to, err)
				}
				continue
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s -> %s: want ErrInvalidTransition, got %v", from, to, err)
			}
			cur, _ := s.Get(ctx, "user:1", j.ID)
			if cur.Status != from {
				t.Errorf("%s -> %s refused but status became %s", from, to, cur.Status)
			}
		}
	}
}

func TestUpdateStatusTimestampsAndError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("user:1"))

	p, err := s.UpdateStatus(ctx, j.ID, StatusProvisioning, nil)
	if err != nil || p.StartedAt != nil || p.FinishedAt != nil {
		t.Fatalf("provisioning: %+v err %v", p, err)
	}
	r, err := s.UpdateStatus(ctx, j.ID, StatusRunning, nil)
	if err != nil || r.StartedAt == nil || r.FinishedAt != nil {
		t.Fatalf("running: %+v err %v", r, err)
	}
	f, err := s.UpdateStatus(ctx, j.ID, StatusFailed, &JobError{Code: "image_pull", Message: "manifest unknown"})
	if err != nil || f.FinishedAt == nil || f.StartedAt == nil || !f.StartedAt.Equal(*r.StartedAt) {
		t.Fatalf("failed: %+v err %v", f, err)
	}
	if f.Error == nil || f.Error.Code != "image_pull" || f.Error.Message != "manifest unknown" {
		t.Fatalf("error not stored: %+v", f.Error)
	}
	if f.FinishedAt.Before(*f.StartedAt) {
		t.Fatalf("finished %v before started %v", f.FinishedAt, f.StartedAt)
	}
}

func TestUpdateStatusCancelFromQueuedNeverStarted(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	j := mustCreate(t, s, newJob("user:1"))
	c, err := s.UpdateStatus(context.Background(), j.ID, StatusCancelled, &JobError{Code: "balance_exhausted", Message: "hold could not be extended"})
	if err != nil || c.StartedAt != nil || c.FinishedAt == nil {
		t.Fatalf("cancelled: %+v err %v", c, err)
	}
}

func TestUpdateStatusUnknownJob(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.UpdateStatus(ctx, NewJobID(time.Now()), StatusProvisioning, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	j := mustCreate(t, s, newJob("user:1"))
	if _, err := s.UpdateStatus(ctx, j.ID, "bogus", nil); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("want ErrInvalidStatus, got %v", err)
	}
}

// Two workers racing the same transition: exactly one wins.
func TestUpdateStatusRaceHasOneWinner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("user:1"))
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateStatus(ctx, j.ID, StatusCancelled, nil)
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d racers won, want 1", wins)
	}
}
