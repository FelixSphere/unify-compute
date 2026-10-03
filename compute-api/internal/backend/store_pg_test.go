package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/jackc/pgx/v5"
)

// TestBackendWithPostgresStore runs a job through launch, UP, exit and
// teardown with the real store, so placement and the status timestamps are
// checked against the schema rather than a stand-in. Skips unless
// COMPUTE_TEST_DATABASE_URL is set.
func TestBackendWithPostgresStore(t *testing.T) {
	dbURL := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "backend_test_" + hex.EncodeToString(suffix[:])
	admin, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	created, _, err := st.Create(ctx, store.NewJob{Owner: "user:1", SKU: "a10g-24gb-x1",
		Spec: json.RawMessage(`{"image":"ghcr.io/acme/embed:1.4","command":["python","run.py"],"max_runtime_seconds":7200}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	fake, j := h.fake, h.job
	j.ID = created.ID
	b := New(&Client{BaseURL: fake.URL, Token: fake.Token, UserHash: "unifycmp"}, st, &recSink{}, Config{Logf: t.Logf})
	if err := b.Launch(ctx, j); err != nil {
		t.Fatal(err)
	}
	name := ClusterName(j.ID, 1)
	step := func() {
		t.Helper()
		if err := b.Step(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	fake.Up(name, "AWS", "us-east-1", "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)")
	step()
	got, err := st.Get(ctx, "user:1", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var p store.Placement
	if err := json.Unmarshal(got.Placement, &p); err != nil || p != (store.Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"}) {
		t.Fatalf("placement %s (%v)", got.Placement, err)
	}
	if got.Status != store.StatusRunning || got.StartedAt == nil {
		t.Fatalf("status %s started_at %v", got.Status, got.StartedAt)
	}

	fake.SubmitJob(name)
	exit := time.Now()
	fake.SetJob(name, "FAILED", nil, &exit)
	step()
	step()
	got, _ = st.Get(ctx, "user:1", j.ID)
	if got.Status != store.StatusFailed || got.Error == nil || got.Error.Code != CodeNonZeroExit || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}
	if got.Placement == nil {
		t.Fatal("placement must survive the terminal transition")
	}
	if fake.Exists(name) {
		t.Fatal("cluster not torn down")
	}
}
