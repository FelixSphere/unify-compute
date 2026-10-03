package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWellFormedKey(t *testing.T) {
	good := "uck_" + strings.Repeat("aZ09", 10)
	for key, want := range map[string]bool{
		good:                                   true,
		"uck_" + strings.Repeat("a", 39):       false,
		"uck_" + strings.Repeat("a", 41):       false,
		"uck_" + strings.Repeat("a", 39) + "-": false,
		"uck_" + strings.Repeat("a", 39) + "é": false,
		"UCK_" + strings.Repeat("a", 40):       false,
		"sk-" + strings.Repeat("a", 41):        false,
		"":                                     false,
	} {
		if got := WellFormedKey(key); got != want {
			t.Errorf("WellFormedKey(%q) = %v", key, got)
		}
	}
}

func TestNewKeySecretShapeAndSpread(t *testing.T) {
	seen := map[string]bool{}
	counts := map[byte]int{}
	for range 500 {
		k, err := newKeySecret()
		if err != nil {
			t.Fatal(err)
		}
		if !WellFormedKey(k) || seen[k] {
			t.Fatalf("key %q (well-formed %v, repeated %v)", k, WellFormedKey(k), seen[k])
		}
		seen[k] = true
		for i := len(KeyPrefix); i < len(k); i++ {
			counts[k[i]]++
		}
	}
	// 20000 characters over 62 symbols: about 323 each. Every symbol shows
	// up, and none is wildly over- or under-drawn (a modulo bias would put
	// the first symbols near twice the rest).
	if len(counts) != 62 {
		t.Fatalf("only %d distinct characters", len(counts))
	}
	for c, n := range counts {
		if n < 200 || n > 460 {
			t.Errorf("character %q drawn %d times", c, n)
		}
	}
}

func TestValidateKeyName(t *testing.T) {
	for _, ok := range []string{"laptop", "  CI runner  ", "prod/batch #2", "训练服务器", strings.Repeat("x", 64), "a&b \"quoted\""} {
		if _, err := ValidateKeyName(ok); err != nil {
			t.Errorf("ValidateKeyName(%q) = %v", ok, err)
		}
	}
	if got, _ := ValidateKeyName("  CI runner  "); got != "CI runner" {
		t.Errorf("not trimmed: %q", got)
	}
	for _, bad := range []string{
		"", "   ", strings.Repeat("x", 65),
		"evil\r\nBcc: victim@example.com", "tab\there", "nul\x00",
		"<script>alert(1)</script>", "a>b",
		"admin\u202Etxt.exe", "zero\u200Bwidth",
		string([]byte{0xff, 0xfe}),
	} {
		if _, err := ValidateKeyName(bad); !errors.Is(err, ErrInvalidKeyName) {
			t.Errorf("ValidateKeyName(%q) = %v, want invalid_name", bad, err)
		}
	}
}

func TestNormalizeScopes(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"jobs"}, "[jobs]"},
		{[]string{"read"}, "[read]"},
		{[]string{"read", "jobs", "read"}, "[jobs read]"},
	} {
		got, err := NormalizeScopes(c.in)
		if err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("NormalizeScopes(%v) = %v, %v", c.in, got, err)
		}
	}
	for _, bad := range [][]string{nil, {}, {"admin"}, {"jobs", "billing"}, {"JOBS"}} {
		if _, err := NormalizeScopes(bad); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("NormalizeScopes(%v) = %v", bad, err)
		}
	}
}

func TestKeyLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "keys@example.com")

	k, secret, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, " laptop ", []string{"read", "jobs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !WellFormedKey(secret) || k.Display != secret[:12] || k.Name != "laptop" || fmt.Sprint(k.Scopes) != "[jobs read]" ||
		k.CreatedBy != u.ID || k.ExpiresAt != nil || k.RevokedAt != nil || k.LastUsedAt != nil {
		t.Fatalf("created %+v (secret %q)", k, secret)
	}
	// Only the hash and the display prefix are stored.
	var stored []byte
	var rows int
	if err := f.db.QueryRow(ctx, `SELECT secret_hash FROM api_keys WHERE id = $1`, k.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256([]byte(secret)); string(stored) != string(sum[:]) {
		t.Fatal("secret_hash is not SHA-256 of the key")
	}
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE position($1 in name || display) > 0`, secret[12:]).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("secret stored in clear (%d rows, %v)", rows, err)
	}

	vk, err := f.svc.VerifyKey(ctx, secret)
	if err != nil || vk.KeyID != k.ID || vk.AccountID != u.AccountID || !vk.Has(ScopeJobs) || !vk.Has(ScopeRead) {
		t.Fatalf("verify %+v %v", vk, err)
	}
	if _, err := f.svc.VerifyKey(ctx, "uck_"+strings.Repeat("Q", 40)); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("unknown key: %v", err)
	}

	list, err := f.svc.ListKeys(ctx, u.AccountID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(f.clock.now()) {
		t.Fatalf("list after use %+v %v", list, err)
	}

	other := f.activate(t, "other@example.com")
	if _, _, err := f.svc.RevokeKey(ctx, other.AccountID, k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke by another account: %v", err)
	}
	if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
		t.Fatalf("still valid after a foreign revoke: %v", err)
	}
	f.clock.advance(time.Second)
	rk, changed, err := f.svc.RevokeKey(ctx, u.AccountID, k.ID)
	if err != nil || !changed || rk.RevokedAt == nil {
		t.Fatalf("revoke %+v %v %v", rk, changed, err)
	}
	if _, err := f.svc.VerifyKey(ctx, secret); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("revoked key verified: %v", err)
	}
	if again, changed, err := f.svc.RevokeKey(ctx, u.AccountID, k.ID); err != nil || changed || !again.RevokedAt.Equal(*rk.RevokedAt) {
		t.Fatalf("second revoke %+v %v %v", again, changed, err)
	}
}

func TestLastUsedIsWrittenAtMostOnceAMinute(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "busy@example.com")
	k, secret, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "busy", []string{"jobs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lastUsed := func() time.Time {
		var at time.Time
		if err := f.db.QueryRow(ctx, `SELECT last_used_at FROM api_keys WHERE id = $1`, k.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	first := f.clock.now()
	if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		f.clock.advance(10 * time.Second)
		if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	if got := lastUsed(); !got.Equal(first) {
		t.Fatalf("last_used_at %v, want %v: rewritten within the minute", got, first)
	}
	f.clock.advance(10 * time.Second)
	if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if got := lastUsed(); !got.Equal(f.clock.now()) {
		t.Fatalf("last_used_at %v, want %v after a minute", got, f.clock.now())
	}
}

func TestExpiredKeysAndClosedAccountsAreRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "expiry@example.com")
	past := f.clock.now()
	if _, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "late", []string{"jobs"}, &past); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("expiry now: %v", err)
	}
	soon := f.clock.now().Add(time.Hour)
	_, secret, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "temp", []string{"read"}, &soon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Hour)
	if _, err := f.svc.VerifyKey(ctx, secret); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("expired key: %v", err)
	}

	_, live, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "live", []string{"jobs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE accounts SET status = 'closed', closed_at = $2 WHERE id = $1`, u.AccountID, f.clock.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.VerifyKey(ctx, live); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("key of a closed account: %v", err)
	}
	if _, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "more", []string{"jobs"}, nil); !errors.Is(err, ErrSuspended) {
		t.Fatalf("create on a closed account: %v", err)
	}
}

func TestSuspendedAccountKeepsReadingButCannotCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "suspended@example.com")
	_, secret, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "k", []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE accounts SET status = 'suspended', suspended_reason = 'operator' WHERE id = $1`, u.AccountID); err != nil {
		t.Fatal(err)
	}
	// Suspension refuses submits at the gate (account_suspended), not the
	// key: the customer can still read their jobs.
	if _, err := f.svc.VerifyKey(ctx, secret); err != nil {
		t.Fatalf("suspended account's key: %v", err)
	}
	if _, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "k2", []string{"read"}, nil); !errors.Is(err, ErrSuspended) {
		t.Fatalf("create while suspended: %v", err)
	}
}

func TestEleventhActiveKeyIsRefusedEvenConcurrently(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "many@example.com")
	var wg sync.WaitGroup
	errs := make(chan error, 2*MaxActiveKeys)
	for i := range 2 * MaxActiveKeys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, fmt.Sprintf("k%d", i), []string{"jobs"}, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var ok, limited int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrKeyLimit):
			limited++
		default:
			t.Fatal(err)
		}
	}
	if ok != MaxActiveKeys || limited != MaxActiveKeys {
		t.Fatalf("%d created, %d refused; want %d and %d", ok, limited, MaxActiveKeys, MaxActiveKeys)
	}
	// Revoked and expired keys do not count.
	keys, err := f.svc.ListKeys(ctx, u.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RevokeKey(ctx, u.AccountID, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "replacement", []string{"jobs"}, nil); err != nil {
		t.Fatalf("create after a revoke: %v", err)
	}
}

func TestKeysMigrationReRuns(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "rerun@example.com")
	if _, _, err := f.svc.CreateKey(ctx, u.AccountID, u.ID, "k", []string{"jobs"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `DELETE FROM schema_migrations WHERE version = '0007_api_keys'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("0007 does not re-run: %v", err)
	}
	if keys, err := f.svc.ListKeys(ctx, u.AccountID); err != nil || len(keys) != 1 {
		t.Fatalf("keys after re-run %v %v", keys, err)
	}
}
