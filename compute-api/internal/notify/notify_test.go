package notify_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func setup(t *testing.T) (*store.Store, *notify.Notifier) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO accounts (id, status) OVERRIDING SYSTEM VALUE VALUES (7, 'active'), (8, 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO users (account_id, email, password_hash, status, email_verified_at) VALUES
		(7, 'ada@example.com', '$argon2id$x', 'active', now()),
		(7, 'bob@example.com', '$argon2id$x', 'pending', NULL),
		(7, 'cy@example.com', '$argon2id$x', 'disabled', now()),
		(8, 'eve@example.com', '$argon2id$x', 'pending', NULL)`); err != nil {
		t.Fatal(err)
	}
	return st, notify.New(st.Pool(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

var key = notify.Key{AccountID: 7, Subject: "job", SubjectID: "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", Message: "job-succeeded"}

// Eight processes see the same trigger at once (a webhook redelivered while
// the first delivery is still sending, two loops after a restart): exactly
// one sends.
func TestConcurrentTriggersSendOnce(t *testing.T) {
	_, n := setup(t)
	var sends atomic.Int32
	var wg sync.WaitGroup
	outcomes := make(chan notify.Outcome, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := n.ToOwners(context.Background(), key, func(context.Context, string) error { sends.Add(1); return nil })
			if err != nil {
				t.Error(err)
			}
			outcomes <- o
		}()
	}
	wg.Wait()
	close(outcomes)
	count := map[notify.Outcome]int{}
	for o := range outcomes {
		count[o]++
	}
	if sends.Load() != 1 || count[notify.Sent] != 1 || count[notify.Skipped] != 7 {
		t.Fatalf("sends %d, outcomes %v", sends.Load(), count)
	}
	if ok, err := n.Sent(context.Background(), key); !ok || err != nil {
		t.Fatalf("Sent = %v %v", ok, err)
	}
	// A later trigger, a day later or after a restart, finds the row.
	if o, _ := n.ToOwners(context.Background(), key, func(context.Context, string) error { t.Fatal("resent"); return nil }); o != notify.Skipped {
		t.Fatalf("outcome %s", o)
	}
}

func TestOnlyActiveOwnersAreMailed(t *testing.T) {
	_, n := setup(t)
	var to []string
	if _, err := n.ToOwners(context.Background(), key, func(_ context.Context, addr string) error { to = append(to, addr); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(to) != 1 || to[0] != "ada@example.com" {
		t.Fatalf("mailed %v", to)
	}
}

// An account without an active owner records nothing, so the message can
// still go out once someone verifies.
func TestNoRecipientRecordsNothing(t *testing.T) {
	_, n := setup(t)
	k := key
	k.AccountID = 8
	o, err := n.ToOwners(context.Background(), k, func(context.Context, string) error { t.Fatal("sent"); return nil })
	if o != notify.NoRecipient || err != nil {
		t.Fatalf("%s %v", o, err)
	}
	if o, _ := n.To(context.Background(), k, []string{"eve@example.com"}, func(context.Context, string) error { return nil }); o != notify.Sent {
		t.Fatalf("a later send with a recipient: %s", o)
	}
}

// A refusal is definite, so the next trigger retries, MaxAttempts in all.
func TestRefusalIsRetriedUpToMaxAttempts(t *testing.T) {
	st, n := setup(t)
	refuse := func(context.Context, string) error { return errors.New("MessageRejected") }
	for i := 1; i <= notify.MaxAttempts; i++ {
		if o, err := n.ToOwners(context.Background(), key, refuse); o != notify.Failed || err == nil {
			t.Fatalf("attempt %d: %s %v", i, o, err)
		}
	}
	if o, _ := n.ToOwners(context.Background(), key, func(context.Context, string) error { t.Fatal("retried past the limit"); return nil }); o != notify.Skipped {
		t.Fatalf("outcome %s", o)
	}
	var state, last string
	var attempts int
	if err := st.Pool().QueryRow(context.Background(), `SELECT state, attempts, last_error FROM notifications`).Scan(&state, &attempts, &last); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || attempts != notify.MaxAttempts || last != "MessageRejected" {
		t.Fatalf("row %s %d %q", state, attempts, last)
	}

	k := key
	k.SubjectID = "job_other"
	_, _ = n.ToOwners(context.Background(), k, refuse)
	if o, _ := n.ToOwners(context.Background(), k, func(context.Context, string) error { return nil }); o != notify.Sent {
		t.Fatalf("retry after one refusal: %s", o)
	}
}

// A claim left by a process that died mid-send is never retried: whether
// SES took the message is unknown, and a missing email beats a duplicate.
func TestAbandonedClaimIsNotResent(t *testing.T) {
	st, n := setup(t)
	if _, err := st.Pool().Exec(context.Background(), `INSERT INTO notifications (account_id, subject_kind, subject_id, message, claimed_at)
		VALUES ($1, $2, $3, $4, now())`, key.AccountID, key.Subject, key.SubjectID, key.Message); err != nil {
		t.Fatal(err)
	}
	if o, _ := n.ToOwners(context.Background(), key, func(context.Context, string) error { t.Fatal("resent"); return nil }); o != notify.Skipped {
		t.Fatalf("outcome %s", o)
	}
}

func TestIncompleteKeyIsRefused(t *testing.T) {
	_, n := setup(t)
	for _, k := range []notify.Key{{Subject: "job", SubjectID: "x", Message: "m"}, {AccountID: 7, SubjectID: "x", Message: "m"},
		{AccountID: 7, Subject: "job", Message: "m"}, {AccountID: 7, Subject: "job", SubjectID: "x"}} {
		if _, err := n.To(context.Background(), k, []string{"ada@example.com"}, func(context.Context, string) error { return nil }); err == nil {
			t.Errorf("%+v accepted", k)
		}
	}
}

func TestNotificationsMigrationReRuns(t *testing.T) {
	st, n := setup(t)
	ctx := context.Background()
	if _, err := n.ToOwners(ctx, key, func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0011_notifications'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("0011 does not re-run: %v", err)
	}
	if ok, _ := n.Sent(ctx, key); !ok {
		t.Fatal("the re-run lost the record")
	}
	for name, sql := range map[string]string{
		"sent without sent_at":   `UPDATE notifications SET state = 'sent', sent_at = NULL`,
		"an unknown state":       `UPDATE notifications SET state = 'queued'`,
		"a subject kind in caps": `INSERT INTO notifications (account_id, subject_kind, subject_id, message, claimed_at) VALUES (7, 'Job', 'x', 'm', now())`,
	} {
		if _, err := st.Pool().Exec(ctx, sql); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
