// Package store persists compute jobs and their idempotency keys in Postgres.
// It is compute-api's own database (docs/architecture.md); nothing here reads
// or writes the console's.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound              = errors.New("store: job not found")
	ErrInvalidTransition     = errors.New("store: status transition not allowed")
	ErrIdempotencyConflict   = errors.New("store: idempotency key reused with a different request")
	ErrInvalidIdempotencyKey = errors.New("store: idempotency key must be 1 to 255 characters")
	ErrInvalidCursor         = errors.New("store: list cursor does not name a job of this owner")
	ErrInvalidStatus         = errors.New("store: unknown job status")
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects and pings. It does not migrate; call Migrate explicitly so a
// deploy decides when schema changes run.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	return openConfig(ctx, cfg)
}

func openConfig(ctx context.Context, cfg *pgxpool.Config) (*Store, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Pool is the connection pool, for the ledger, which keeps its own tables in
// the same database and runs its own transactions.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }
