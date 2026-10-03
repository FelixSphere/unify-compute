package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestAuditAppendsAndRefusesChanges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	id, err := f.svc.Audit(ctx, AuditEvent{ActorType: ActorUser, ActorID: "1", AccountID: 1, Action: ActionLogin,
		Client: ClientInfo{IP: "203.0.113.9", UserAgent: "curl"}, Detail: map[string]any{"via": "password"}})
	if err != nil || !strings.HasPrefix(id, "ae_") {
		t.Fatalf("audit: %q %v", id, err)
	}
	if _, err := f.svc.Audit(ctx, AuditEvent{ActorType: ActorSystem, Action: ActionLoginFailed}); err != nil {
		t.Fatalf("audit without an account: %v", err)
	}

	for _, stmt := range []string{
		`UPDATE audit_events SET action = 'logout' WHERE id = '` + id + `'`,
		`DELETE FROM audit_events WHERE id = '` + id + `'`,
		`TRUNCATE audit_events`,
	} {
		_, err := f.db.Exec(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: want the append-only refusal, got %v", stmt, err)
		}
	}
	var n int
	var detail map[string]any
	if err := f.db.QueryRow(ctx, `SELECT count(*), (SELECT detail FROM audit_events WHERE id = $1) FROM audit_events`, id).Scan(&n, &detail); err != nil {
		t.Fatal(err)
	}
	if n != 2 || detail["via"] != "password" {
		t.Fatalf("rows after refused changes: %d, detail %v", n, detail)
	}
}

func TestAdminAuditNeedsAReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.Audit(ctx, AuditEvent{ActorType: ActorAdmin, ActorID: "ops", AccountID: 1, Action: ActionAdminCredit, Reason: "  "}); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("admin action without a reason: %v", err)
	}
	// The table refuses it too, for a writer that skips this package.
	_, err := f.db.Exec(ctx, `INSERT INTO audit_events (id, actor_type, action) VALUES ('ae_01J0000000000000000000000A', 'admin', 'admin_credit')`)
	if err == nil {
		t.Fatal("table accepted an admin row without a reason")
	}
	if _, err := f.svc.Audit(ctx, AuditEvent{ActorType: ActorAdmin, ActorID: "ops", AccountID: 1, Action: ActionAdminCredit, Reason: "first slice funding"}); err != nil {
		t.Fatalf("admin action with a reason: %v", err)
	}
}

func TestAuditTxRollsBackWithTheCaller(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := pgx.BeginFunc(ctx, f.db, func(tx pgx.Tx) error {
		if _, err := f.svc.AuditTx(ctx, tx, AuditEvent{ActorType: ActorAdmin, ActorID: "ops", Action: ActionAdminCredit, Reason: "r"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit row outlived its rolled-back transaction: %d %v", n, err)
	}
}

func TestAccountsMigrationReRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.signup(t, "kept@example.com")
	if _, err := f.db.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0005_accounts'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("re-running 0005 over its own objects: %v", err)
	}
	if _, err := f.svc.UserByEmail(ctx, "kept@example.com"); err != nil {
		t.Fatalf("re-run lost a row: %v", err)
	}
}
