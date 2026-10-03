// Package accounts is Unify Compute's own identity store (ADR-0011,
// docs/spec/accounts.md): accounts, users, sessions, verification codes,
// reset tokens, auth throttling, password hashing and the audit log, on the
// tables of store migration 0005.
//
// It holds no HTTP code. The dashboard routes (task A4) call it and map its
// *Error values onto the job API's error envelope; Code and Param are the
// spec's, so that mapping is a status lookup.
package accounts

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Error is a refusal a person can act on. Code and Param are the values of
// docs/spec/accounts.md "Errors"; Message is the sentence the page shows.
type Error struct {
	Code    string
	Param   string
	Message string
}

func (e *Error) Error() string { return "accounts: " + e.Code }

var (
	ErrInvalidEmail       = &Error{"invalid_email", "email", "Enter a valid email address."}
	ErrWeakPassword       = &Error{"weak_password", "password", "Use 12 to 128 characters, at least 5 different, and not your email address."}
	ErrPasswordMismatch   = &Error{"password_mismatch", "password_confirm", "Passwords don't match."}
	ErrInvalidCode        = &Error{"invalid_code", "code", "That code is not valid. Check the latest email or request a new code."}
	ErrInvalidToken       = &Error{"invalid_token", "token", "This reset link is invalid or has expired. Request a new one."}
	ErrInvalidCredentials = &Error{"invalid_credentials", "", "Incorrect email or password."}
	ErrEmailUnverified    = &Error{"email_unverified", "", "Verify your email address first. We can send a new code."}
	ErrAccountDisabled    = &Error{"account_disabled", "", "This account is disabled. Contact us if you think this is a mistake."}
	ErrSessionRequired    = &Error{"session_required", "", "Log in to continue."}
)

var (
	// ErrEmailTaken is not shown to anyone: signup answers 202 either way
	// and the address gets the "someone tried to sign up" email instead.
	ErrEmailTaken = errors.New("accounts: email already registered")
	ErrNotFound   = errors.New("accounts: not found")
)

// Lifetimes from ADR-0011 (D8 for sessions).
const (
	SessionIdle     = 12 * time.Hour
	SessionAbsolute = 7 * 24 * time.Hour
	ReauthWindow    = 10 * time.Minute
	CodeTTL         = 15 * time.Minute
	CodeMaxAttempts = 5
	ResetTokenTTL   = time.Hour
	PendingTTL      = 7 * 24 * time.Hour

	// lastSeenGranularity bounds session writes to one a minute per session.
	lastSeenGranularity = time.Minute
)

const (
	UserPending  = "pending"
	UserActive   = "active"
	UserDisabled = "disabled"
	UserClosed   = "closed"

	AccountActive    = "active"
	AccountSuspended = "suspended"
	AccountClosed    = "closed"
)

type Account struct {
	ID              int64
	Status          string
	ComputeEnabled  bool
	Available       int64
	Held            int64
	TopupMinCents   int
	TopupMaxCents   int
	BalanceMaxCents int
	TopupsPerDay    int
	SuspendedReason string
	CreatedAt       time.Time
}

// Owner is the owner string jobs, quotas and events key on.
func (a Account) Owner() string { return OwnerFor(a.ID) }

type User struct {
	ID                int64
	AccountID         int64
	Role              string
	Email             string
	Status            string
	EmailVerifiedAt   *time.Time
	LastLoginAt       *time.Time
	PasswordChangedAt *time.Time
	CreatedAt         time.Time

	passwordHash string
}

// PasswordHasher is what Service needs from Hasher; tests substitute a
// counting wrapper to prove an unknown email costs one hash like a known one.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error)
	VerifyDummy(ctx context.Context, password string) error
}

type Config struct {
	// ComputeEnabledDefault is ACCOUNTS_COMPUTE_DEFAULT: compute_enabled for
	// new accounts. Off while starting small (ADR-0011 D3).
	ComputeEnabledDefault bool
	// Hasher defaults to NewHasher(DefaultParams).
	Hasher PasswordHasher
	// Now defaults to time.Now. Every timestamp this package writes or
	// compares comes from it, not from the database clock, so tests can move
	// time.
	Now func() time.Time
}

type Service struct {
	db             *pgxpool.Pool
	hasher         PasswordHasher
	now            func() time.Time
	computeDefault bool
}

func New(db *pgxpool.Pool, cfg Config) *Service {
	s := &Service{db: db, hasher: cfg.Hasher, now: cfg.Now, computeDefault: cfg.ComputeEnabledDefault}
	if s.hasher == nil {
		s.hasher = NewHasher(DefaultParams)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// HashPassword hashes with the service's hasher, for callers that validate
// first and store later (signup, reset, change).
func (s *Service) HashPassword(ctx context.Context, password string) (string, error) {
	return s.hasher.Hash(ctx, password)
}

const userColumns = `u.id, u.account_id, u.role, u.email, u.status, u.email_verified_at,
	u.last_login_at, u.password_changed_at, u.created_at, u.password_hash`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.AccountID, &u.Role, &u.Email, &u.Status, &u.EmailVerifiedAt,
		&u.LastLoginAt, &u.PasswordChangedAt, &u.CreatedAt, &u.passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

const accountColumns = `a.id, a.status, a.compute_enabled, a.available, a.held, a.topup_min_cents,
	a.topup_max_cents, a.balance_max_cents, a.topups_per_day, coalesce(a.suspended_reason, ''), a.created_at`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Status, &a.ComputeEnabled, &a.Available, &a.Held, &a.TopupMinCents,
		&a.TopupMaxCents, &a.BalanceMaxCents, &a.TopupsPerDay, &a.SuspendedReason, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// CreatePending creates an account and its pending owner. email is
// normalised and validated here; password must already have passed
// ValidateNewPassword and be hashed with HashPassword. A registered address
// is ErrEmailTaken.
func (s *Service) CreatePending(ctx context.Context, email, passwordHash string) (User, Account, error) {
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return User{}, Account{}, err
	}
	now := s.now()
	var u User
	var a Account
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		a, err = scanAccount(tx.QueryRow(ctx, `INSERT INTO accounts AS a (compute_enabled, created_at)
			VALUES ($1, $2) RETURNING `+accountColumns, s.computeDefault, now))
		if err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users AS u (account_id, email, password_hash, created_at)
			VALUES ($1, $2, $3, $4) RETURNING `+userColumns, a.ID, email, passwordHash, now))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return err
	})
	if err != nil {
		return User{}, Account{}, err
	}
	return u, a, nil
}

// UserByEmail looks up by the normalised form of email.
func (s *Service) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.email = $1`, NormalizeEmail(email)))
}

func (s *Service) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id))
}

func (s *Service) AccountByID(ctx context.Context, id int64) (Account, error) {
	return scanAccount(s.db.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts a WHERE a.id = $1`, id))
}

// Authenticate is login's password step. An unknown email and a wrong
// password are both ErrInvalidCredentials and both cost one argon2id
// verification. email_unverified and account_disabled are only said after
// the correct password, so they reveal nothing to someone guessing. A hash
// made with old parameters is replaced on success.
func (s *Service) Authenticate(ctx context.Context, email, password string) (User, error) {
	u, err := s.UserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		if err := s.hasher.VerifyDummy(ctx, password); err != nil {
			return User{}, err
		}
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if err := s.checkPassword(ctx, u, password); err != nil {
		return User{}, err
	}
	switch u.Status {
	case UserPending:
		return User{}, ErrEmailUnverified
	case UserActive:
	default:
		return User{}, ErrAccountDisabled
	}
	now := s.now()
	if _, err := s.db.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, u.ID, now); err != nil {
		return User{}, err
	}
	u.LastLoginAt = &now
	return u, nil
}

// VerifyPassword checks the password of a user who already holds a session
// (reauth, password change, account closing). Wrong is ErrInvalidCredentials.
func (s *Service) VerifyPassword(ctx context.Context, userID int64, password string) error {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.checkPassword(ctx, u, password)
}

func (s *Service) checkPassword(ctx context.Context, u User, password string) error {
	ok, rehash, err := s.hasher.Verify(ctx, password, u.passwordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if rehash {
		h, err := s.hasher.Hash(ctx, password)
		if err != nil {
			return err
		}
		// Only if unchanged meanwhile: a concurrent reset must win.
		if _, err := s.db.Exec(ctx, `UPDATE users SET password_hash = $3 WHERE id = $1 AND password_hash = $2`,
			u.ID, u.passwordHash, h); err != nil {
			return err
		}
	}
	return nil
}

// ChangePassword stores a new hash and ends every session of the user except
// keepSession (the cookie value of the session that made the change; empty
// ends them all).
func (s *Service) ChangePassword(ctx context.Context, userID int64, passwordHash, keepSession string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, password_changed_at = $3 WHERE id = $1`,
			userID, passwordHash, s.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return deleteSessions(ctx, tx, userID, keepSession)
	})
}

// PurgeUnverified deletes accounts whose owner is still pending PendingTTL
// after signup, freeing the address. Sessions, codes and tokens go with the
// user by cascade; audit rows stay. Returns the number of accounts removed.
func (s *Service) PurgeUnverified(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM accounts a USING users u
		WHERE u.account_id = a.id AND u.status = 'pending' AND u.created_at < $1`, s.now().Add(-PendingTTL))
	return tag.RowsAffected(), err
}
