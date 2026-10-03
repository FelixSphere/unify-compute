package accounts

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Rule is one throttle: at most Limit counted attempts per subject in a
// fixed window of Window that opens at the first attempt. Bucket names the
// rule in auth_attempts keys ("bucket:subject") and must be unique per rule.
type Rule struct {
	Bucket string
	Limit  int
	Window time.Duration
}

// The limits of ADR-0011 §8 and spec/accounts.md. Subjects are the client
// IP, the normalised email, or the account id, as the names say. The
// per-account login limit is only a backstop, so there is no hard lockout.
var (
	LoginPerIP           = Rule{"login_ip", 20, 10 * time.Minute}
	LoginPerAccount      = Rule{"login_account", 50, 10 * time.Minute}
	SignupPerIP          = Rule{"signup_ip", 5, time.Hour}
	ForgotPerIP          = Rule{"forgot_ip", 5, time.Hour}
	VerifyPerIP          = Rule{"verify_ip", 20, time.Hour}
	ResendPerEmailMinute = Rule{"resend_email_minute", 1, time.Minute}
	ResendPerEmailHour   = Rule{"resend_email_hour", 5, time.Hour}
)

// Decision is a throttle's answer. Count is the attempts counted in the
// current window. When Allowed is false, RetryAfter is the time until the
// window closes, for the Retry-After header (whole seconds, rounded up, at
// least 1).
type Decision struct {
	Allowed    bool
	Count      int
	RetryAfter time.Duration
}

func (r Rule) key(subject string) string { return r.Bucket + ":" + subject }

func decide(allowed bool, count int, expires, now time.Time) Decision {
	d := Decision{Allowed: allowed, Count: count}
	if !allowed {
		d.RetryAfter = max(time.Duration(math.Ceil(expires.Sub(now).Seconds()))*time.Second, time.Second)
	}
	return d
}

// Hit counts one attempt and says whether it is within the limit. An
// attempt over the limit is still counted; it does not extend the window.
// A row whose own window has passed starts a new window, whatever any other
// rule's rows are doing.
func (s *Service) Hit(ctx context.Context, r Rule, subject string) (Decision, error) {
	now := s.now()
	var count int
	var expires time.Time
	err := s.db.QueryRow(ctx, `INSERT INTO auth_attempts AS a (key, count, window_start, expires_at)
		VALUES ($1, 1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET
			count        = CASE WHEN a.expires_at <= $2 THEN 1  ELSE a.count + 1 END,
			window_start = CASE WHEN a.expires_at <= $2 THEN $2 ELSE a.window_start END,
			expires_at   = CASE WHEN a.expires_at <= $2 THEN $3 ELSE a.expires_at END
		RETURNING count, expires_at`, r.key(subject), now, now.Add(r.Window)).Scan(&count, &expires)
	if err != nil {
		return Decision{}, err
	}
	return decide(count <= r.Limit, count, expires, now), nil
}

// Check says whether one more attempt would be allowed, without counting.
// For limits on failures (wrong codes): Check before, Hit only on failure.
func (s *Service) Check(ctx context.Context, r Rule, subject string) (Decision, error) {
	now := s.now()
	var count int
	var expires time.Time
	err := s.db.QueryRow(ctx, `SELECT count, expires_at FROM auth_attempts WHERE key = $1 AND expires_at > $2`,
		r.key(subject), now).Scan(&count, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{Allowed: true}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	return decide(count < r.Limit, count, expires, now), nil
}
