package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
)

// Emails a Stripe event causes are queued in payment_emails inside the
// event's transaction, so an applied refund or dispute always has its
// messages, and sent after the commit. Each row is (event id, kind,
// recipient): the Stripe event id is the idempotency key, and a message is
// claimed before it is sent and marked sent after, so it goes out at most
// once. One that failed is unclaimed and retried when the event is
// delivered again, or by RetryEmails.

// claimTTL: a claim older than this is taken to have died with its process.
const claimTTL = 15 * time.Minute

func (s *Service) queue(ctx context.Context, tx pgx.Tx, eventID, kind, to string, accountID int64, detail any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO payment_emails (event_id, kind, recipient, account_id, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, eventID, kind, to, accountID, b, s.now())
	return err
}

// queueAccount queues a message to every verified user of the account.
func (s *Service) queueAccount(ctx context.Context, tx pgx.Tx, eventID, kind string, accountID int64, detail any) error {
	rows, err := tx.Query(ctx, `SELECT email FROM users WHERE account_id = $1 AND status = $2 ORDER BY id`, accountID, accounts.UserActive)
	if err != nil {
		return err
	}
	to, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, addr := range to {
		if err := s.queue(ctx, tx, eventID, kind, addr, accountID, detail); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) queueOperator(ctx context.Context, tx pgx.Tx, eventID, kind string, accountID int64, n mailer.OperatorNotice) error {
	return s.queue(ctx, tx, eventID, kind, s.operator, accountID, n)
}

// sendEmails sends the event's unsent messages in the background.
func (s *Service) sendEmails(eventID string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.deliver(ctx, eventID); err != nil {
			s.log.Error("payment emails", "event", eventID, "err", err)
		}
	}()
}

// RetryEmails sends every message still unsent, of any event. For the
// operator, or a later sweep; nothing calls it on a timer yet.
func (s *Service) RetryEmails(ctx context.Context) error { return s.deliver(ctx, "") }

func (s *Service) deliver(ctx context.Context, eventID string) error {
	if s.mail == nil {
		var n int
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM payment_emails WHERE ($1 = '' OR event_id = $1) AND sent_at IS NULL`, eventID).Scan(&n)
		if n > 0 {
			s.log.Error("payment emails queued but this deployment has no mailer; not sent", "event", eventID, "count", n)
		}
		return nil
	}
	type pending struct {
		event, kind, to string
		detail          []byte
	}
	rows, err := s.db.Query(ctx, `SELECT event_id, kind, recipient, detail FROM payment_emails
		WHERE ($1 = '' OR event_id = $1) AND sent_at IS NULL ORDER BY created_at, kind, recipient`, eventID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var p pending
		err := r.Scan(&p.event, &p.kind, &p.to, &p.detail)
		return p, err
	})
	if err != nil {
		return err
	}
	for _, p := range list {
		now := s.now()
		tag, err := s.db.Exec(ctx, `UPDATE payment_emails SET claimed_at = $4
			WHERE event_id = $1 AND kind = $2 AND recipient = $3 AND sent_at IS NULL AND (claimed_at IS NULL OR claimed_at < $5)`,
			p.event, p.kind, p.to, now, now.Add(-claimTTL))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		key := []any{p.event, p.kind, p.to}
		if err := s.send(ctx, p.kind, p.to, p.detail); err != nil {
			s.log.Error("payment email not sent; retried on the event's next delivery", "event", p.event, "kind", p.kind, "err", err)
			if _, uerr := s.db.Exec(ctx, `UPDATE payment_emails SET claimed_at = NULL WHERE event_id = $1 AND kind = $2 AND recipient = $3`, key...); uerr != nil {
				return uerr
			}
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE payment_emails SET sent_at = $4 WHERE event_id = $1 AND kind = $2 AND recipient = $3`,
			append(key, s.now())...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) send(ctx context.Context, kind, to string, detail []byte) error {
	switch kind {
	case mailRefundIssued:
		var r mailer.Refund
		if err := json.Unmarshal(detail, &r); err != nil {
			return err
		}
		return s.mail.RefundIssued(ctx, to, r)
	case mailAccountSuspended:
		var v mailer.Suspension
		if err := json.Unmarshal(detail, &v); err != nil {
			return err
		}
		return s.mail.AccountSuspended(ctx, to, v)
	case mailOpsDisputeOpened, mailOpsDisputeClosed, mailOpsRefundNegative:
		var n mailer.OperatorNotice
		if err := json.Unmarshal(detail, &n); err != nil {
			return err
		}
		return s.mail.Operator(ctx, to, n)
	}
	return fmt.Errorf("payments: unknown email kind %q", kind)
}
