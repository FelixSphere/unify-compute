package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OwnerFor is the owner string of an account's jobs: "account:<id>".
func OwnerFor(accountID int64) string { return "account:" + strconv.FormatInt(accountID, 10) }

// Session is a dashboard session. Token is the cookie value and is set only
// by CreateSession; the database holds its SHA-256.
type Session struct {
	Token      string
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ReauthAt   time.Time
	ExpiresAt  time.Time
}

// ReauthFresh reports whether the password was entered in this session
// within ReauthWindow, which key creation, password change and closing need.
func (s Session) ReauthFresh(now time.Time) bool { return now.Sub(s.ReauthAt) < ReauthWindow }

// ClientInfo is recorded with sessions and audit events. IP is the last
// X-Forwarded-For entry (the ALB's), resolved by the caller.
type ClientInfo struct {
	IP        string
	UserAgent string
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func digest(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateSession starts a session for a user who has just proved the
// password (login) or the address (verify). Rotation at login is the
// caller's: end the old session with DeleteSession.
func (s *Service) CreateSession(ctx context.Context, userID int64, c ClientInfo) (Session, error) {
	token, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	sess := Session{Token: token, UserID: userID, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now,
		ReauthAt: now, ExpiresAt: now.Add(SessionAbsolute)}
	_, err = s.db.Exec(ctx, `INSERT INTO sessions
		(id_hash, user_id, csrf_token, created_at, last_seen_at, reauth_at, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $4, $4, $5, $6, $7)`,
		digest(token), userID, csrf, now, sess.ExpiresAt, c.IP, c.UserAgent)
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// LookupSession returns the live session for a cookie value and its user.
// Past the absolute lifetime, idle longer than SessionIdle, or belonging to a
// user who is no longer active, it is ErrSessionRequired. last_seen_at is
// moved forward at most once a minute.
func (s *Service) LookupSession(ctx context.Context, token string) (Session, User, error) {
	if token == "" {
		return Session{}, User{}, ErrSessionRequired
	}
	now := s.now()
	var sess Session
	row := s.db.QueryRow(ctx, `SELECT s.user_id, s.csrf_token, s.created_at, s.last_seen_at, s.reauth_at, s.expires_at, `+userColumns+`
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = $1 AND s.expires_at > $2 AND s.last_seen_at > $3 AND u.status = 'active'`,
		digest(token), now, now.Add(-SessionIdle))
	var u User
	err := row.Scan(&sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.LastSeenAt, &sess.ReauthAt, &sess.ExpiresAt,
		&u.ID, &u.AccountID, &u.Role, &u.Email, &u.Status, &u.EmailVerifiedAt,
		&u.LastLoginAt, &u.PasswordChangedAt, &u.CreatedAt, &u.passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrSessionRequired
	}
	if err != nil {
		return Session{}, User{}, err
	}
	sess.Token = token
	if now.Sub(sess.LastSeenAt) >= lastSeenGranularity {
		if _, err := s.db.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id_hash = $1`, digest(token), now); err != nil {
			return Session{}, User{}, err
		}
		sess.LastSeenAt = now
	}
	return sess, u, nil
}

// MarkReauth records that the password was just entered in this session.
func (s *Service) MarkReauth(ctx context.Context, token string) error {
	tag, err := s.db.Exec(ctx, `UPDATE sessions SET reauth_at = $2 WHERE id_hash = $1`, digest(token), s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionRequired
	}
	return nil
}

// DeleteSession ends one session (logout, rotation). Ending a session that
// does not exist is not an error.
func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, digest(token))
	return err
}

// DeleteUserSessions ends every session of a user except keep (a cookie
// value; empty keeps none).
func (s *Service) DeleteUserSessions(ctx context.Context, userID int64, keep string) error {
	return deleteSessions(ctx, s.db, userID, keep)
}

// execer is satisfied by both the pool and a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func deleteSessions(ctx context.Context, db execer, userID int64, keep string) error {
	var keepHash []byte
	if keep != "" {
		keepHash = digest(keep)
	}
	_, err := db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR id_hash <> $2)`, userID, keepHash)
	return err
}
