package payments

import (
	"context"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func TestBonusMigrationReRunsAndHoldsItsRules(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	db := st.Pool()
	now := time.Now()
	id := store.NewID("tu_", now)
	if _, err := db.Exec(ctx, `INSERT INTO topups (id, account_id, user_id, amount_cents, bonus_cents, created_at) VALUES ($1, 1, 1, 10000, 500, $2)`, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO refund_requests (id, account_id, user_id, topup_id, amount_cents, created_at) VALUES ($1, 1, 1, $2, 100, $3)`,
		store.NewID("rr_", now), id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0015_bonus_and_refund_requests'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("0015 does not re-run: %v", err)
	}
	for name, sql := range map[string]string{
		"more bonus reversed than earned":    `UPDATE topups SET bonus_reversed_cents = 501`,
		"a second open request for a top-up": `INSERT INTO refund_requests (id, account_id, user_id, topup_id, amount_cents, created_at) VALUES ('` + store.NewID("rr_", now) + `', 1, 1, '` + id + `', 1, now())`,
		"refunded without its event":         `UPDATE refund_requests SET status = 'refunded', resolved_at = now()`,
		"closed without resolved_at":         `UPDATE refund_requests SET status = 'declined'`,
		"a reason over 500 characters":       `UPDATE refund_requests SET reason = repeat('x', 501)`,
	} {
		if _, err := db.Exec(ctx, sql); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO balance_entries (id, account_id, kind, available_delta, held_delta, available_after, held_after, ref_type, ref_id, idempotency_key)
		VALUES ($1, 1, 'topup_bonus', 500, 0, 500, 0, 'topup', $2, $3)`, store.NewID("be_", now), id, "topup_bonus:"+id); err != nil {
		t.Errorf("topup_bonus refused: %v", err)
	}
}
