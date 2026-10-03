package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const purposeVerify = "verify"

func codeHash(id, code string) []byte {
	sum := sha256.Sum256([]byte(id + ":" + code))
	return sum[:]
}

// IssueVerifyCode supersedes any earlier code of the user and returns a new
// 6-digit code for the email. Only a pending user gets one: for anyone else
// it is ErrNotFound, and resend answers 202 without sending.
func (s *Service) IssueVerifyCode(ctx context.Context, userID int64) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	now := s.now()
	id := store.NewID("ec_", now)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// The user row lock serialises concurrent resends, which would
		// otherwise both supersede and then collide on the live-code index.
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != UserPending) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE email_codes SET superseded_at = $3
			WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL AND superseded_at IS NULL`,
			userID, purposeVerify, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO email_codes (id, user_id, purpose, code_hash, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, id, userID, purposeVerify, codeHash(id, code), now.Add(CodeTTL), now)
		return err
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// VerifyEmail checks a code for an address and, if it is the live code,
// activates the user. Unknown address, user not pending, no live code, a
// wrong, expired, exhausted or superseded code: all ErrInvalidCode, so the
// answer says nothing about which. A wrong code counts against the live
// code's CodeMaxAttempts even though the call fails.
func (s *Service) VerifyEmail(ctx context.Context, email, code string) (User, error) {
	email = NormalizeEmail(email)
	now := s.now()
	var u User
	var wrong bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.email = $1 FOR UPDATE`, email))
		if errors.Is(err, ErrNotFound) || (err == nil && u.Status != UserPending) {
			return ErrInvalidCode
		}
		if err != nil {
			return err
		}
		var id string
		var hash []byte
		err = tx.QueryRow(ctx, `SELECT id, code_hash FROM email_codes
			WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL AND superseded_at IS NULL
			  AND expires_at > $3 AND attempts < $4`,
			u.ID, purposeVerify, now, CodeMaxAttempts).Scan(&id, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidCode
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(codeHash(id, code), hash) != 1 {
			wrong = true
			_, err := tx.Exec(ctx, `UPDATE email_codes SET attempts = attempts + 1 WHERE id = $1`, id)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE email_codes SET used_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'active', email_verified_at = $2 WHERE id = $1`, u.ID, now); err != nil {
			return err
		}
		u.Status, u.EmailVerifiedAt = UserActive, &now
		return nil
	})
	if err != nil {
		return User{}, err
	}
	if wrong {
		return User{}, ErrInvalidCode
	}
	return u, nil
}

const purposeReset = "reset"

// IssueResetToken supersedes any earlier reset link of the user and returns
// a new token for the link's fragment. Forgot-password sends one only to an
// active or pending user; for anyone else it is ErrNotFound.
func (s *Service) IssueResetToken(ctx context.Context, userID int64) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != UserPending && status != UserActive) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_tokens SET superseded_at = $3
			WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL AND superseded_at IS NULL`,
			userID, purposeReset, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO auth_tokens (digest, user_id, purpose, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5)`, digest(token), userID, purposeReset, now.Add(ResetTokenTTL), now)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ResetPassword uses a reset token: it stores passwordHash, marks the
// address verified if it was not (following the link proves control of it),
// and ends every session of the user, in one transaction. It does not log
// in. The caller validates the new password against the user's email first;
// ResetTokenUser gives that email without using the token.
func (s *Service) ResetPassword(ctx context.Context, token, passwordHash string) (User, error) {
	now := s.now()
	var u User
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var userID int64
		err := tx.QueryRow(ctx, `SELECT user_id FROM auth_tokens
			WHERE digest = $1 AND purpose = $2 AND used_at IS NULL AND superseded_at IS NULL AND expires_at > $3
			FOR UPDATE`, digest(token), purposeReset, now).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.id = $1 FOR UPDATE`, userID))
		if err != nil {
			return err
		}
		if u.Status != UserPending && u.Status != UserActive {
			return ErrInvalidToken
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_tokens SET used_at = $2 WHERE digest = $1`, digest(token), now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, password_changed_at = $3, status = 'active',
			email_verified_at = coalesce(email_verified_at, $3) WHERE id = $1`, u.ID, passwordHash, now); err != nil {
			return err
		}
		if u.EmailVerifiedAt == nil {
			u.EmailVerifiedAt = &now
		}
		u.Status, u.PasswordChangedAt = UserActive, &now
		return deleteSessions(ctx, tx, u.ID, "")
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// ResetTokenUser returns the user a live reset token belongs to, without
// using it, so the caller can check the new password against the address.
func (s *Service) ResetTokenUser(ctx context.Context, token string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM auth_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.digest = $1 AND t.purpose = $2 AND t.used_at IS NULL AND t.superseded_at IS NULL AND t.expires_at > $3
		  AND u.status IN ('pending', 'active')`, digest(token), purposeReset, s.now()))
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrInvalidToken
	}
	return u, err
}

// PurgeExpired deletes sessions, codes, reset tokens and throttle rows that
// can no longer be used. Meant for the hourly sweep. Each row is judged
// against its own lifetime.
func (s *Service) PurgeExpired(ctx context.Context) error {
	now := s.now()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2`, []any{now, now.Add(-SessionIdle)}},
		{`DELETE FROM email_codes WHERE expires_at <= $1`, []any{now}},
		{`DELETE FROM auth_tokens WHERE expires_at <= $1`, []any{now}},
		{`DELETE FROM auth_attempts WHERE expires_at <= $1`, []any{now}},
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(ctx, st.sql, st.args...); err != nil {
			return err
		}
	}
	return nil
}
