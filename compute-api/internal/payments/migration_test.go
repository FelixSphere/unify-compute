package payments

import (
	"context"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func TestTopupsMigrationReRunsAndHoldsItsRules(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	db := st.Pool()
	now := time.Now()
	id := store.NewID("tu_", now)
	if _, err := db.Exec(ctx, `INSERT INTO topups (id, account_id, user_id, amount_cents, created_at) VALUES ($1, 1, 1, 2000, $2)`, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0008_topups'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("0008 does not re-run: %v", err)
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM topups`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("top-ups after re-run: %d %v", n, err)
	}
	for name, sql := range map[string]string{
		"a second open top-up for the account": `INSERT INTO topups (id, account_id, user_id, amount_cents, created_at) VALUES ('` + store.NewID("tu_", now) + `', 1, 1, 1000, now())`,
		"credited without paid_at":             `UPDATE topups SET status = 'credited'`,
		"a currency other than usd":            `UPDATE topups SET currency = 'eur'`,
		"a zero amount":                        `UPDATE topups SET amount_cents = 0`,
		"an id that is not a top-up's":         `INSERT INTO topups (id, account_id, user_id, amount_cents, created_at) VALUES ('cs_1', 2, 1, 1000, now())`,
	} {
		if _, err := db.Exec(ctx, sql); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
