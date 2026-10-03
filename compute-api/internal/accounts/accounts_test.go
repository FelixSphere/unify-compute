package accounts

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingHasher counts argon2id evaluations, real or dummy.
type countingHasher struct {
	*Hasher
	verifies atomic.Int64
}

func (h *countingHasher) Verify(ctx context.Context, pw, enc string) (bool, bool, error) {
	h.verifies.Add(1)
	return h.Hasher.Verify(ctx, pw, enc)
}

func (h *countingHasher) VerifyDummy(ctx context.Context, pw string) error {
	h.verifies.Add(1)
	return h.Hasher.VerifyDummy(ctx, pw)
}

type fixture struct {
	store  *store.Store
	svc    *Service
	db     *pgxpool.Pool
	clock  *clock
	hasher *countingHasher
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := storetest.New(t)
	c := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	h := &countingHasher{Hasher: NewHasher(cheapParams)}
	return &fixture{store: st, svc: New(st.Pool(), Config{Hasher: h, Now: c.now}), db: st.Pool(), clock: c, hasher: h}
}

const testPassword = "correct horse battery"

func (f *fixture) signup(t *testing.T, email string) (User, Account) {
	t.Helper()
	ctx := context.Background()
	h, err := f.svc.HashPassword(ctx, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, a, err := f.svc.CreatePending(ctx, email, h)
	if err != nil {
		t.Fatalf("create pending %s: %v", email, err)
	}
	return u, a
}

func (f *fixture) activate(t *testing.T, email string) User {
	t.Helper()
	u, _ := f.signup(t, email)
	code, err := f.svc.IssueVerifyCode(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, err = f.svc.VerifyEmail(context.Background(), email, code)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return u
}

func TestCreatePending(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u, a := f.signup(t, "  New.User@Example.com ")
	if u.Email != "new.user@example.com" || u.Status != UserPending || u.Role != "owner" || u.AccountID != a.ID {
		t.Fatalf("user: %+v", u)
	}
	if a.Status != AccountActive || a.ComputeEnabled || a.Available != 0 || a.Held != 0 ||
		a.TopupMinCents != 1000 || a.TopupMaxCents != 10000 || a.BalanceMaxCents != 20000 || a.TopupsPerDay != 3 {
		t.Fatalf("account defaults: %+v", a)
	}
	if a.Owner() != "account:"+strconv.FormatInt(a.ID, 10) {
		t.Fatalf("owner: %s", a.Owner())
	}
	h, _ := f.svc.HashPassword(ctx, testPassword)
	if _, _, err := f.svc.CreatePending(ctx, "NEW.USER@example.com", h); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("second signup with the same address: want ErrEmailTaken, got %v", err)
	}
	var accounts int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM accounts`).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("the refused signup left an account behind: %d %v", accounts, err)
	}
	if _, _, err := f.svc.CreatePending(ctx, "not-an-address", h); err != ErrInvalidEmail {
		t.Fatalf("invalid address: %v", err)
	}
}

func TestComputeEnabledDefaultIsConfigured(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	svc := New(st.Pool(), Config{ComputeEnabledDefault: true, Hasher: NewHasher(cheapParams)})
	h, _ := svc.HashPassword(context.Background(), testPassword)
	_, a, err := svc.CreatePending(context.Background(), "on@example.com", h)
	if err != nil || !a.ComputeEnabled {
		t.Fatalf("compute_enabled with the default on: %+v %v", a, err)
	}
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.signup(t, "pending@example.com")

	check := func(name, email, pw string, want error) {
		t.Helper()
		before := f.hasher.verifies.Load()
		_, err := f.svc.Authenticate(ctx, email, pw)
		if err != want {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
		if n := f.hasher.verifies.Load() - before; n != 1 {
			t.Fatalf("%s: %d argon2id verifications, want exactly 1", name, n)
		}
	}
	check("unknown email", "nobody@example.com", testPassword, ErrInvalidCredentials)
	check("wrong password", "pending@example.com", "wrong password here", ErrInvalidCredentials)
	check("pending, right password", "pending@example.com", testPassword, ErrEmailUnverified)

	code, _ := f.svc.IssueVerifyCode(ctx, u.ID)
	if _, err := f.svc.VerifyEmail(ctx, "pending@example.com", code); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Authenticate(ctx, " PENDING@example.com", testPassword)
	if err != nil || got.ID != u.ID || got.LastLoginAt == nil {
		t.Fatalf("active login: %+v %v", got, err)
	}

	if _, err := f.db.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	check("disabled, wrong password", "pending@example.com", "wrong password here", ErrInvalidCredentials)
	check("disabled, right password", "pending@example.com", testPassword, ErrAccountDisabled)
}

func TestLoginRehashesWhenParamsChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "rehash@example.com")

	raised := cheapParams
	raised.Time = 3
	svc := New(f.db, Config{Hasher: NewHasher(raised), Now: f.clock.now})
	if _, err := svc.Authenticate(ctx, "rehash@example.com", testPassword); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := f.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, u.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "$m=64,t=3,p=1$") {
		t.Fatalf("hash not upgraded at login: %s", stored)
	}
	if _, err := svc.Authenticate(ctx, "rehash@example.com", testPassword); err != nil {
		t.Fatalf("login with the upgraded hash: %v", err)
	}
	if err := svc.VerifyPassword(ctx, u.ID, "wrong password here"); err != ErrInvalidCredentials {
		t.Fatalf("VerifyPassword wrong: %v", err)
	}
}

func TestVerifyEmailCode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.signup(t, "code@example.com")

	code, err := f.svc.IssueVerifyCode(ctx, u.ID)
	if err != nil || len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("code %q: %v", code, err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := range CodeMaxAttempts {
		if _, err := f.svc.VerifyEmail(ctx, "code@example.com", wrong); err != ErrInvalidCode {
			t.Fatalf("wrong code %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.VerifyEmail(ctx, "code@example.com", code); err != ErrInvalidCode {
		t.Fatalf("right code after %d wrong ones must be exhausted: %v", CodeMaxAttempts, err)
	}

	old, _ := f.svc.IssueVerifyCode(ctx, u.ID)
	code, _ = f.svc.IssueVerifyCode(ctx, u.ID)
	if old != code {
		if _, err := f.svc.VerifyEmail(ctx, "code@example.com", old); err != ErrInvalidCode {
			t.Fatalf("superseded code: %v", err)
		}
	}

	f.clock.advance(CodeTTL)
	if _, err := f.svc.VerifyEmail(ctx, "code@example.com", code); err != ErrInvalidCode {
		t.Fatalf("expired code: %v", err)
	}

	code, _ = f.svc.IssueVerifyCode(ctx, u.ID)
	if _, err := f.svc.VerifyEmail(ctx, "nobody@example.com", code); err != ErrInvalidCode {
		t.Fatalf("unknown address: %v", err)
	}
	got, err := f.svc.VerifyEmail(ctx, "Code@Example.com", code)
	if err != nil || got.Status != UserActive || got.EmailVerifiedAt == nil {
		t.Fatalf("right code: %+v %v", got, err)
	}
	if _, err := f.svc.VerifyEmail(ctx, "code@example.com", code); err != ErrInvalidCode {
		t.Fatalf("used code: %v", err)
	}
	if _, err := f.svc.IssueVerifyCode(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code for an active user: %v", err)
	}
}

func TestConcurrentResendsLeaveOneLiveCode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.signup(t, "race@example.com")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.svc.IssueVerifyCode(ctx, u.ID); err != nil {
				t.Errorf("issue: %v", err)
			}
		}()
	}
	wg.Wait()
	var live int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM email_codes WHERE user_id = $1 AND superseded_at IS NULL`, u.ID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("live codes: %d %v", live, err)
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "reset@example.com")
	s1, _ := f.svc.CreateSession(ctx, u.ID, ClientInfo{})
	s2, _ := f.svc.CreateSession(ctx, u.ID, ClientInfo{})

	old, _ := f.svc.IssueResetToken(ctx, u.ID)
	token, err := f.svc.IssueResetToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	newHash, _ := f.svc.HashPassword(ctx, "a brand new password")
	if _, err := f.svc.ResetPassword(ctx, old, newHash); err != ErrInvalidToken {
		t.Fatalf("superseded token: %v", err)
	}
	if got, err := f.svc.ResetTokenUser(ctx, token); err != nil || got.ID != u.ID {
		t.Fatalf("token user: %+v %v", got, err)
	}
	got, err := f.svc.ResetPassword(ctx, token, newHash)
	if err != nil || got.PasswordChangedAt == nil {
		t.Fatalf("reset: %+v %v", got, err)
	}
	for _, s := range []Session{s1, s2} {
		if _, _, err := f.svc.LookupSession(ctx, s.Token); err != ErrSessionRequired {
			t.Fatalf("session survived the reset: %v", err)
		}
	}
	if _, err := f.svc.ResetPassword(ctx, token, newHash); err != ErrInvalidToken {
		t.Fatalf("used token: %v", err)
	}
	if _, err := f.svc.ResetTokenUser(ctx, token); err != ErrInvalidToken {
		t.Fatalf("used token user: %v", err)
	}
	if _, err := f.svc.Authenticate(ctx, "reset@example.com", "a brand new password"); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}

	expiring, _ := f.svc.IssueResetToken(ctx, u.ID)
	f.clock.advance(ResetTokenTTL)
	if _, err := f.svc.ResetPassword(ctx, expiring, newHash); err != ErrInvalidToken {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := f.svc.ResetPassword(ctx, "not-a-token", newHash); err != ErrInvalidToken {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestResetVerifiesAPendingUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u, _ := f.signup(t, "pendingreset@example.com")
	token, err := f.svc.IssueResetToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := f.svc.HashPassword(ctx, "a brand new password")
	got, err := f.svc.ResetPassword(ctx, token, h)
	if err != nil || got.Status != UserActive || got.EmailVerifiedAt == nil {
		t.Fatalf("reset of a pending user: %+v %v", got, err)
	}
	if _, err := f.svc.Authenticate(ctx, "pendingreset@example.com", "a brand new password"); err != nil {
		t.Fatalf("login after reset: %v", err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IssueResetToken(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reset link for a disabled user: %v", err)
	}
}

func TestSessionLifetimes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "session@example.com")

	s, err := f.svc.CreateSession(ctx, u.ID, ClientInfo{IP: "203.0.113.7", UserAgent: "test"})
	if err != nil || s.Token == "" || s.CSRFToken == "" || s.Token == s.CSRFToken {
		t.Fatalf("create: %+v %v", s, err)
	}
	var stored int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id_hash = $1 OR csrf_token = $2`, []byte(s.Token), s.Token).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("cookie value stored in clear: %d %v", stored, err)
	}

	got, gu, err := f.svc.LookupSession(ctx, s.Token)
	if err != nil || gu.ID != u.ID || got.CSRFToken != s.CSRFToken {
		t.Fatalf("lookup: %+v %+v %v", got, gu, err)
	}
	if !got.ReauthFresh(f.clock.now()) {
		t.Fatal("a session just created by a password is fresh")
	}

	// last_seen_at moves at most once a minute.
	f.clock.advance(30 * time.Second)
	got, _, _ = f.svc.LookupSession(ctx, s.Token)
	if !got.LastSeenAt.Equal(s.LastSeenAt) {
		t.Fatal("last_seen_at written within a minute")
	}
	f.clock.advance(31 * time.Second)
	got, _, _ = f.svc.LookupSession(ctx, s.Token)
	if !got.LastSeenAt.Equal(f.clock.now()) {
		t.Fatal("last_seen_at not written after a minute")
	}
	if got.ReauthFresh(f.clock.now().Add(ReauthWindow)) {
		t.Fatal("reauth stays fresh past the window")
	}

	// Idle for 12 h ends it.
	f.clock.advance(SessionIdle)
	if _, _, err := f.svc.LookupSession(ctx, s.Token); err != ErrSessionRequired {
		t.Fatalf("idle session: %v", err)
	}

	// Active every few hours, it still ends 7 days after creation.
	s, _ = f.svc.CreateSession(ctx, u.ID, ClientInfo{})
	for elapsed := time.Duration(0); elapsed+6*time.Hour < SessionAbsolute; elapsed += 6 * time.Hour {
		f.clock.advance(6 * time.Hour)
		if _, _, err := f.svc.LookupSession(ctx, s.Token); err != nil {
			t.Fatalf("active session at %v: %v", elapsed+6*time.Hour, err)
		}
	}
	f.clock.advance(6 * time.Hour)
	if _, _, err := f.svc.LookupSession(ctx, s.Token); err != ErrSessionRequired {
		t.Fatalf("session past 7 days: %v", err)
	}

	if _, _, err := f.svc.LookupSession(ctx, ""); err != ErrSessionRequired {
		t.Fatalf("empty cookie: %v", err)
	}
}

func TestReauthAndPasswordChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "change@example.com")
	current, _ := f.svc.CreateSession(ctx, u.ID, ClientInfo{})
	other, _ := f.svc.CreateSession(ctx, u.ID, ClientInfo{})

	f.clock.advance(ReauthWindow)
	got, _, _ := f.svc.LookupSession(ctx, current.Token)
	if got.ReauthFresh(f.clock.now()) {
		t.Fatal("fresh after the window")
	}
	if err := f.svc.MarkReauth(ctx, current.Token); err != nil {
		t.Fatal(err)
	}
	got, _, _ = f.svc.LookupSession(ctx, current.Token)
	if !got.ReauthFresh(f.clock.now()) {
		t.Fatal("not fresh after reauth")
	}

	h, _ := f.svc.HashPassword(ctx, "another fine password")
	if err := f.svc.ChangePassword(ctx, u.ID, h, current.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.LookupSession(ctx, current.Token); err != nil {
		t.Fatalf("the session that changed the password ended: %v", err)
	}
	if _, _, err := f.svc.LookupSession(ctx, other.Token); err != ErrSessionRequired {
		t.Fatalf("another session survived the change: %v", err)
	}
	if err := f.svc.DeleteSession(ctx, current.Token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.LookupSession(ctx, current.Token); err != ErrSessionRequired {
		t.Fatalf("logged-out session: %v", err)
	}
	if err := f.svc.MarkReauth(ctx, current.Token); err != ErrSessionRequired {
		t.Fatalf("reauth on an ended session: %v", err)
	}
}

func TestDisablingAUserEndsItsSessions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	u := f.activate(t, "disable@example.com")
	s, _ := f.svc.CreateSession(ctx, u.ID, ClientInfo{})
	if _, err := f.db.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.LookupSession(ctx, s.Token); err != ErrSessionRequired {
		t.Fatalf("session of a disabled user: %v", err)
	}
}

func TestPurgeUnverified(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	stale, _ := f.signup(t, "stale@example.com")
	if _, err := f.svc.IssueVerifyCode(ctx, stale.ID); err != nil {
		t.Fatal(err)
	}
	f.activate(t, "kept@example.com")
	f.clock.advance(PendingTTL - time.Minute)
	f.signup(t, "fresh@example.com")
	f.clock.advance(2 * time.Minute)

	n, err := f.svc.PurgeUnverified(ctx)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if _, err := f.svc.UserByEmail(ctx, "stale@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale pending user kept: %v", err)
	}
	if _, err := f.svc.AccountByID(ctx, stale.AccountID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale account kept: %v", err)
	}
	for _, e := range []string{"kept@example.com", "fresh@example.com"} {
		if _, err := f.svc.UserByEmail(ctx, e); err != nil {
			t.Fatalf("%s purged: %v", e, err)
		}
	}
	f.signup(t, "stale@example.com") // the address is free again
}
