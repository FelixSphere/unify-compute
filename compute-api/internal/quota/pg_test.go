package quota

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/jackc/pgx/v5"
)

// openSchema gives the test a migrated store in a schema of its own, as the
// store package's tests do, through the exported API only: pgx passes an
// unrecognised URL parameter such as search_path to the server.
func openSchema(t *testing.T, dbURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "quota_test_" + hex.EncodeToString(suffix[:])

	admin, err := pgx.Connect(ctx, dbURL)
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

	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}
