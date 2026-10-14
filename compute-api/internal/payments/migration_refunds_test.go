package payments

import (
	"context"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func TestRefundsMigrationReRunsAndHoldsItsRules(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	db := st.Pool()
	now := time.Now()
	var id int64
	if err := db.QueryRow(ctx, `INSERT INTO accounts DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// 0012 lets a refund take an active account's balance below zero.
	if _, err := db.Exec(ctx, `UPDATE accounts SET available = -5 WHERE id = $1`, id); err != nil {
		t.Fatalf("negative balance on an active account: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO topup_disputes (id, topup_id, account_id, amount_cents, opened_event_id, created_at)
		VALUES ('dp_1', $1, 1, 1000, 'evt_1', $2)`, store.NewID("tu_", now), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0012_refunds_disputes'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("0012 does not re-run: %v", err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM topup_disputes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("disputes after re-run: %d %v", n, err)
	}
	for name, sql := range map[string]string{
		"a dispute closed without closed_at": `UPDATE topup_disputes SET status = 'won'`,
		"an unknown dispute status":          `UPDATE topup_disputes SET status = 'pending'`,
		"a zero disputed amount":             `UPDATE topup_disputes SET amount_cents = 0`,
		"a dispute id that is not Stripe's":  `INSERT INTO topup_disputes (id, topup_id, account_id, amount_cents, opened_event_id, created_at) VALUES ('x', 't', 1, 1, 'e', now())`,
		"the same email twice for one event": `INSERT INTO payment_emails (event_id, kind, recipient, account_id, created_at) VALUES ('evt_1', 'refund_issued', 'a@b.c', 1, now()), ('evt_1', 'refund_issued', 'a@b.c', 1, now())`,
	} {
		if _, err := db.Exec(ctx, sql); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
