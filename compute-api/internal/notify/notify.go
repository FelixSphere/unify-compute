// Package notify makes a transactional email go out once per thing it is
// about, however often its trigger fires: a Stripe webhook redelivered, a
// job's end observed again after a restart, two processes on one database.
// It knows nothing about what the message says; the caller passes a function
// that sends it to one address (normally a method of *mailer.Mailer).
//
// Usage, for a message about an account's own object:
//
//	n.ToOwners(ctx, notify.Key{AccountID: 7, Subject: "refund", SubjectID: "re_…", Message: "refund-issued"},
//		func(ctx context.Context, to string) error { return mail.RefundIssued(ctx, to, r) })
//
// and for an address that is not yet a user (an invitation), To with the
// address. Neither returns an error to act on: a mail failure must never fail
// the job transition, webhook or request that triggered it, so both log and
// report the outcome only for tests and for callers that want to print it.
//
// The record is the notifications table (migration 0011). A row is the claim,
// written before the first address is mailed, so the guarantee is at most
// once: a crash between the claim and SES's answer loses that email rather
// than risking a second copy, which is Unify Data's rule for its sends too
// (updates.py, "A send happens once"). A definite refusal from SES is not
// ambiguous, so it is recorded as failed and the next trigger for the same
// key tries again, up to MaxAttempts.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

// Key names one message about one thing. Subject is the kind of thing
// ("job", "topup", "refund", "invitation", "balance"); SubjectID its id, or
// for a message that can recur (a low-balance warning per downward crossing)
// an id that names the occurrence. Message is the mailer's kind.
type Key struct {
	AccountID int64
	Subject   string
	SubjectID string
	Message   string
}

// Outcome is what a call did.
type Outcome string

const (
	// Sent: this call claimed the key and at least one address was accepted.
	Sent Outcome = "sent"
	// Skipped: the key was already sent, is being sent by someone else, was
	// left claimed by a process that died mid-send, or failed MaxAttempts
	// times.
	Skipped Outcome = "skipped"
	// Failed: this call claimed the key and every address was refused; the
	// row is failed and a later call may try again.
	Failed Outcome = "failed"
	// NoRecipient: the account has no active owner. Nothing is recorded, so
	// the message goes out if a later trigger finds one.
	NoRecipient Outcome = "no_recipient"
)

// MaxAttempts bounds retries of a key SES keeps refusing.
const MaxAttempts = 5

// SendFunc sends the message to one address.
type SendFunc func(ctx context.Context, to string) error

type Notifier struct {
	db  *pgxpool.Pool
	log *slog.Logger
	now func() time.Time
}

func New(db *pgxpool.Pool, log *slog.Logger, now func() time.Time) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Notifier{db: db, log: log, now: now}
}

// ToOwners sends to every active owner of the account: the people who answer
// for its money and its jobs (accounts.md, roles).
func (n *Notifier) ToOwners(ctx context.Context, k Key, send SendFunc) (Outcome, error) {
	to, err := n.Owners(ctx, k.AccountID)
	if err != nil {
		n.log.Error("notify: owner lookup", "message", k.Message, "subject", k.Subject, "id", k.SubjectID, "account", k.AccountID, "err", err)
		return "", err
	}
	return n.To(ctx, k, to, send)
}

// Owners is the account's active owners' addresses, oldest user first.
func (n *Notifier) Owners(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := n.db.Query(ctx, `SELECT email FROM users WHERE account_id = $1 AND status = $2 AND role = 'owner' ORDER BY id`,
		accountID, accounts.UserActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// To claims k and sends to each address. Sent means at least one address was
// accepted: the claim covers the message, not each address, so a refusal for
// one owner is logged and not retried once another owner has it.
func (n *Notifier) To(ctx context.Context, k Key, to []string, send SendFunc) (Outcome, error) {
	log := n.log.With("message", k.Message, "subject", k.Subject, "id", k.SubjectID, "account", k.AccountID)
	if len(to) == 0 {
		log.Warn("notify: no recipient; nothing sent")
		return NoRecipient, nil
	}
	claimed, err := n.claim(ctx, k)
	if err != nil {
		log.Error("notify: claim", "err", err)
		return "", err
	}
	if !claimed {
		return Skipped, nil
	}
	var errs []error
	delivered := 0
	for _, addr := range to {
		if err := send(ctx, addr); err != nil {
			errs = append(errs, err)
			continue
		}
		delivered++
	}
	sendErr := errors.Join(errs...)
	// The outcome is written even if the caller's context has ended, or the
	// row would read as an unknown outcome that is never retried.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if delivered > 0 {
		var last *string
		if sendErr != nil {
			s := truncate(sendErr.Error())
			last = &s
			log.Error("notify: some recipients refused", "delivered", delivered, "of", len(to), "err", sendErr)
		}
		_, err := n.db.Exec(wctx, `UPDATE notifications SET state = 'sent', sent_at = $5, delivered = $6, last_error = $7
			WHERE account_id = $1 AND subject_kind = $2 AND subject_id = $3 AND message = $4`,
			k.AccountID, k.Subject, k.SubjectID, k.Message, n.now(), delivered, last)
		if err != nil {
			log.Error("notify: sent, but the record was not updated; it stays claimed and is not resent", "err", err)
		}
		return Sent, nil
	}
	log.Error("notify: not sent", "err", sendErr)
	_, err = n.db.Exec(wctx, `UPDATE notifications SET state = 'failed', delivered = 0, last_error = $5
		WHERE account_id = $1 AND subject_kind = $2 AND subject_id = $3 AND message = $4`,
		k.AccountID, k.Subject, k.SubjectID, k.Message, truncate(sendErr.Error()))
	if err != nil {
		log.Error("notify: record failure", "err", err)
	}
	return Failed, sendErr
}

// claim inserts the row, or takes over a failed one with attempts left.
func (n *Notifier) claim(ctx context.Context, k Key) (bool, error) {
	if k.AccountID <= 0 || k.Subject == "" || k.SubjectID == "" || k.Message == "" {
		return false, fmt.Errorf("notify: incomplete key %+v", k)
	}
	tag, err := n.db.Exec(ctx, `INSERT INTO notifications (account_id, subject_kind, subject_id, message, claimed_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, k.AccountID, k.Subject, k.SubjectID, k.Message, n.now())
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	tag, err = n.db.Exec(ctx, `UPDATE notifications SET state = 'claimed', attempts = attempts + 1, claimed_at = $5
		WHERE account_id = $1 AND subject_kind = $2 AND subject_id = $3 AND message = $4 AND state = 'failed' AND attempts < $6`,
		k.AccountID, k.Subject, k.SubjectID, k.Message, n.now(), MaxAttempts)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Sent reports whether the key has been sent: for tests and for a sweep that
// looks for things not yet notified.
func (n *Notifier) Sent(ctx context.Context, k Key) (bool, error) {
	var ok bool
	err := n.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications
		WHERE account_id = $1 AND subject_kind = $2 AND subject_id = $3 AND message = $4 AND state = 'sent')`,
		k.AccountID, k.Subject, k.SubjectID, k.Message).Scan(&ok)
	return ok, err
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}
