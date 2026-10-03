package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// API keys (ADR-0011 §2, spec/accounts.md "api_keys"): uck_ and 40 base62
// characters, stored as SHA-256 of the whole key, shown once.
const (
	KeyPrefix     = "uck_"
	keySecretLen  = 40
	keyDisplayLen = 8
	MaxActiveKeys = 10
	maxKeyName    = 64

	ScopeJobs = "jobs"
	ScopeRead = "read"

	// lastUsedGranularity bounds last_used_at writes to one a minute per
	// key, so a busy key does not cost a write per request.
	lastUsedGranularity = time.Minute
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var (
	ErrInvalidKeyName = &Error{"invalid_name", "name", "Name the key with 1 to 64 characters, without line breaks, control characters or angle brackets."}
	ErrInvalidScope   = &Error{"invalid_scope", "scopes", "Choose the scopes jobs, read, or both."}
	ErrInvalidExpiry  = &Error{"invalid_expires_at", "expires_at", "The expiry must be in the future."}
	ErrKeyLimit       = &Error{"key_limit_reached", "", "This account already has 10 active API keys. Revoke one before creating another."}
	ErrSuspended      = &Error{"account_suspended", "", "This account is suspended and cannot create API keys. Contact us."}
)

// ErrKeyRejected is a well-formed key that is unknown, revoked, expired or
// belongs to a closed account. The job API answers all four with the same
// 401 invalid_key.
var ErrKeyRejected = errors.New("accounts: key unknown, revoked or expired")

// APIKey is a key's metadata. The secret is never part of it.
type APIKey struct {
	ID         int64
	AccountID  int64
	Name       string
	Display    string
	Scopes     []string
	CreatedBy  int64
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// VerifiedKey is what the job API learns from a key: whose it is and what it
// may do.
type VerifiedKey struct {
	KeyID     int64
	AccountID int64
	Scopes    []string
}

func (k VerifiedKey) Has(scope string) bool { return slices.Contains(k.Scopes, scope) }

// WellFormedKey is the shape check the job API makes before any database
// read: uck_ and exactly 40 base62 characters.
func WellFormedKey(key string) bool {
	secret, ok := strings.CutPrefix(key, KeyPrefix)
	if !ok || len(secret) != keySecretLen {
		return false
	}
	for i := 0; i < len(secret); i++ {
		if strings.IndexByte(base62, secret[i]) < 0 {
			return false
		}
	}
	return true
}

func newKeySecret() (string, error) {
	out := make([]byte, 0, keySecretLen)
	var buf [64]byte
	for len(out) < keySecretLen {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		for _, b := range buf {
			// Rejection sampling: 248 = 4·62, so every character is
			// equally likely.
			if b < 248 && len(out) < keySecretLen {
				out = append(out, base62[b%62])
			}
		}
	}
	return KeyPrefix + string(out), nil
}

// ValidateKeyName trims the name and refuses what could break out of where
// it is shown: control characters (a CR/LF would start a new line in the
// plain-text email), Unicode format characters (bidi overrides that make a
// name read differently from what it is) and angle brackets. The mailer and
// the pages escape it anyway; this keeps the stored value plain.
func ValidateKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxKeyName || !utf8.ValidString(name) {
		return "", ErrInvalidKeyName
	}
	if strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' || r == utf8.RuneError
	}) >= 0 {
		return "", ErrInvalidKeyName
	}
	return name, nil
}

// NormalizeScopes refuses an empty or unknown scope and returns the set in
// a fixed order (jobs before read), without duplicates.
func NormalizeScopes(scopes []string) ([]string, error) {
	var jobs, read bool
	for _, s := range scopes {
		switch s {
		case ScopeJobs:
			jobs = true
		case ScopeRead:
			read = true
		default:
			return nil, ErrInvalidScope
		}
	}
	var out []string
	if jobs {
		out = append(out, ScopeJobs)
	}
	if read {
		out = append(out, ScopeRead)
	}
	if len(out) == 0 {
		return nil, ErrInvalidScope
	}
	return out, nil
}

const keyColumns = `k.id, k.account_id, k.name, k.display, k.scopes, k.created_by, k.created_at, k.expires_at, k.last_used_at, k.revoked_at`

func scanKey(row pgx.Row) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.AccountID, &k.Name, &k.Display, &k.Scopes, &k.CreatedBy, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	return k, err
}

// CreateKey makes a key for the account and returns its metadata and the
// secret, which nothing can return again. name and scopes are validated
// here. A suspended or closed account is ErrSuspended; one with
// MaxActiveKeys live keys is ErrKeyLimit. The account row is locked for the
// count, so two creations at once cannot both take the tenth slot.
func (s *Service) CreateKey(ctx context.Context, accountID, userID int64, name string, scopes []string, expiresAt *time.Time) (APIKey, string, error) {
	name, err := ValidateKeyName(name)
	if err != nil {
		return APIKey{}, "", err
	}
	if scopes, err = NormalizeScopes(scopes); err != nil {
		return APIKey{}, "", err
	}
	now := s.now()
	if expiresAt != nil && !expiresAt.After(now) {
		return APIKey{}, "", ErrInvalidExpiry
	}
	secret, err := newKeySecret()
	if err != nil {
		return APIKey{}, "", err
	}
	display := secret[:len(KeyPrefix)+keyDisplayLen]
	var k APIKey
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != AccountActive {
			return ErrSuspended
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys
			WHERE account_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`,
			accountID, now).Scan(&active); err != nil {
			return err
		}
		if active >= MaxActiveKeys {
			return ErrKeyLimit
		}
		k, err = scanKey(tx.QueryRow(ctx, `INSERT INTO api_keys AS k
			(account_id, name, display, secret_hash, scopes, created_by, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+keyColumns,
			accountID, name, display, digest(secret), scopes, userID, now, expiresAt))
		return err
	})
	if err != nil {
		return APIKey{}, "", err
	}
	return k, secret, nil
}

// ListKeys returns every key of the account, revoked and expired included,
// newest first.
func (s *Service) ListKeys(ctx context.Context, accountID int64) ([]APIKey, error) {
	rows, err := s.db.Query(ctx, `SELECT `+keyColumns+` FROM api_keys k WHERE k.account_id = $1 ORDER BY k.id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey revokes one of the account's keys. A key of another account is
// ErrNotFound, exactly like one that does not exist. Revoking a key that is
// already revoked changes nothing and reports changed false.
func (s *Service) RevokeKey(ctx context.Context, accountID, keyID int64) (k APIKey, changed bool, err error) {
	k, err = scanKey(s.db.QueryRow(ctx, `UPDATE api_keys AS k SET revoked_at = $3
		WHERE k.id = $1 AND k.account_id = $2 AND k.revoked_at IS NULL RETURNING `+keyColumns, keyID, accountID, s.now()))
	if err == nil {
		return k, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return APIKey{}, false, err
	}
	k, err = scanKey(s.db.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys k WHERE k.id = $1 AND k.account_id = $2`, keyID, accountID))
	return k, false, err
}

// VerifyKey resolves a job API key on every request, with no cache, so a
// revoked key is refused on the very next one. The caller has already
// checked WellFormedKey. The row is found by its hash's unique index and the
// stored hash then compared in constant time. last_used_at is moved forward
// at most once a minute.
func (s *Service) VerifyKey(ctx context.Context, key string) (VerifiedKey, error) {
	now := s.now()
	sum := digest(key)
	var (
		vk       VerifiedKey
		stored   []byte
		lastUsed *time.Time
	)
	err := s.db.QueryRow(ctx, `SELECT k.id, k.account_id, k.scopes, k.secret_hash, k.last_used_at
		FROM api_keys k JOIN accounts a ON a.id = k.account_id
		WHERE k.secret_hash = $1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > $2)
			AND a.status <> 'closed'`, sum, now).Scan(&vk.KeyID, &vk.AccountID, &vk.Scopes, &stored, &lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return VerifiedKey{}, ErrKeyRejected
	}
	if err != nil {
		return VerifiedKey{}, err
	}
	if subtle.ConstantTimeCompare(stored, sum) != 1 {
		return VerifiedKey{}, ErrKeyRejected
	}
	if lastUsed == nil || now.Sub(*lastUsed) >= lastUsedGranularity {
		// The condition repeats in SQL so concurrent requests on one key
		// write once between them.
		if _, err := s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = $2
			WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $3)`,
			vk.KeyID, now, now.Add(-lastUsedGranularity)); err != nil {
			return VerifiedKey{}, err
		}
	}
	return vk, nil
}
