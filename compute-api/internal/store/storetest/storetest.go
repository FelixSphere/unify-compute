// Package storetest gives tests outside package store a migrated Store in a
// schema of its own, the same isolation store's own tests use.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/jackc/pgx/v5"
)

// New skips the test unless COMPUTE_TEST_DATABASE_URL names a Postgres the
// test may write.
func New(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	ctx := context.Background()

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "storetest_" + hex.EncodeToString(suffix[:])

	admin, err := pgx.Connect(ctx, raw)
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

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
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
