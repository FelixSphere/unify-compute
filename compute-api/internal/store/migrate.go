package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID serialises Migrate across replicas booting at once. The
// value is arbitrary; it only has to be unique among advisory locks taken in
// this database.
const migrationLockID = 0x636f6d7075746531

var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

type migration struct {
	version  string
	sql      string
	checksum []byte
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if !migrationName.MatchString(e.Name()) {
			return nil, fmt.Errorf("store: migration file %q is not named NNNN_name.sql", e.Name())
		}
		b, err := migrationFiles.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, migration{version: strings.TrimSuffix(e.Name(), ".sql"), sql: string(b), checksum: sum[:]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations, in filename order, each in its own transaction. It is
// safe to run on every boot and from several processes at once.
//
// An applied migration whose file has since changed is an error rather than
// a silent skip: editing a shipped migration means databases created before
// and after the edit differ, and nothing would notice.
func (s *Store) Migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if err := s.apply(ctx, m); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.version, err)
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, m migration) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLockID)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text        PRIMARY KEY,
			checksum   bytea       NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		var applied []byte
		err := tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, m.version).Scan(&applied)
		switch {
		case err == nil:
			if string(applied) != string(m.checksum) {
				return fmt.Errorf("already applied with a different checksum; add a new migration instead of editing this one")
			}
			return nil
		case err != pgx.ErrNoRows:
			return err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`, m.version, m.checksum)
		return err
	})
}
