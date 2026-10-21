package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Customers' refund requests (ADR-0013 §2, spec/accounts.md "Refund
// request"). A customer asks from the Billing page; nothing moves until the
// operator refunds with `admin topup refund`, whose charge.refunded closes
// the request (applyRefund), or declines it with a message.

// Refund request statuses.
const (
	RequestOpen      = "open"
	RequestRefunded  = "refunded"
	RequestDeclined  = "declined"
	RequestCancelled = "cancelled"
)

// MaxRequestReason bounds a customer's reason, in characters.
const MaxRequestReason = 500

// Email kinds of refund requests, queued in payment_emails under the
// request's id.
const (
	mailOpsRefundRequested = "ops_refund_requested"
	mailRefundDeclined     = "refund_declined"
)

// CustomerRequest is one refund request.
type CustomerRequest struct {
	ID          string
	AccountID   int64
	UserID      int64
	TopupID     string
	AmountCents int
	Reason      string
	Status      string
	Resolution  string
	CreatedAt   time.Time
	ResolvedAt  *time.Time
}

var (
	ErrNotRefundable = &Error{Status: http.StatusConflict, Type: "conflict", Code: "topup_not_refundable",
		Message: "This top-up has nothing left to refund."}
	ErrRequestOpen = &Error{Status: http.StatusConflict, Type: "conflict", Code: "refund_request_open",
		Message: "There is already a refund request for this top-up. We'll email you when it's handled."}
	ErrRequestClosed = &Error{Status: http.StatusConflict, Type: "conflict", Code: "refund_request_closed",
		Message: "This refund request has already been handled."}
	ErrReasonTooLong = &Error{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_reason",
		Message: fmt.Sprintf("Keep the reason under %d characters.", MaxRequestReason), Param: "reason"}
)

func amountTooLarge(refundable int64) *Error {
	return &Error{Status: http.StatusBadRequest, Type: "invalid_request", Code: "invalid_amount", Param: "amount_usd",
		Message: "You can ask for at most USD " + CentsUSD(int(refundable)) + " back from this top-up."}
}

// CreateRefundRequest records a request against one of the account's
// top-ups and emails the operator. An empty amount asks for everything still
// refundable. Nothing is refunded here.
func (s *Service) CreateRefundRequest(ctx context.Context, u accounts.User, topupID, amountUSD, reason string, c accounts.ClientInfo) (CustomerRequest, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxRequestReason {
		return CustomerRequest{}, ErrReasonTooLong
	}
	var asked int64
	if strings.TrimSpace(amountUSD) != "" {
		c, ok := ParseAmount(amountUSD)
		if !ok || c == 0 {
			return CustomerRequest{}, ErrInvalidAmount
		}
		asked = int64(c)
	}
	var rr CustomerRequest
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		t, err := lockTopup(ctx, tx, topupID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && t.accountID != u.AccountID) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var refundable int64
		if t.status == StatusCredited || t.status == StatusPartiallyRefunded {
			refundable = t.amount - t.refunded - t.disputed
		}
		if refundable <= 0 {
			return ErrNotRefundable
		}
		if asked == 0 {
			asked = refundable
		}
		if asked > refundable {
			return amountTooLarge(refundable)
		}
		now := s.now()
		rr = CustomerRequest{ID: store.NewID("rr_", now), AccountID: u.AccountID, UserID: u.ID, TopupID: t.id, AmountCents: int(asked),
			Reason: reason, Status: RequestOpen, CreatedAt: now}
		_, err = tx.Exec(ctx, `INSERT INTO refund_requests (id, account_id, user_id, topup_id, amount_cents, reason, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, rr.ID, rr.AccountID, rr.UserID, rr.TopupID, rr.AmountCents, rr.Reason, now)
		if isUniqueViolation(err) {
			return ErrRequestOpen
		}
		if err != nil {
			return err
		}
		if _, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorUser, ActorID: strconv.FormatInt(u.ID, 10),
			AccountID: u.AccountID, Action: accounts.ActionRefundRequested, Client: c,
			Detail: map[string]any{"refund_request_id": rr.ID, "topup_id": t.id, "amount_usd": CentsUSD(int(asked))}}); err != nil {
			return err
		}
		why := reason
		if why == "" {
			why = "(none given)"
		}
		return s.queueOperator(ctx, tx, rr.ID, mailOpsRefundRequested, u.AccountID, mailer.OperatorNotice{
			Title: "refund requested on " + t.id,
			Body:  "A customer asked for USD " + CentsUSD(int(asked)) + " of a top-up back on their card. Nothing has been refunded yet.",
			Action: "Check the account, then run `compute-api admin topup refund " + t.id + " --usd " + CentsUSD(int(asked)) + " --reason …` " +
				"or `compute-api admin refund-request decline " + rr.ID + " --message … --reason …`.",
			Facts: [][2]string{{"Request", rr.ID}, {"Account", strconv.FormatInt(u.AccountID, 10)}, {"Requested by", u.Email},
				{"Top-up", t.id + " (USD " + CentsUSD(int(t.amount)) + ")"}, {"Amount asked", "USD " + CentsUSD(int(asked))},
				{"Still refundable", "USD " + CentsUSD(int(refundable))}, {"Reason", why}},
		})
	})
	if err != nil {
		return CustomerRequest{}, err
	}
	s.sendEmails(rr.ID)
	return rr, nil
}

// CancelRefundRequest withdraws the account's own open request.
func (s *Service) CancelRefundRequest(ctx context.Context, u accounts.User, id string, c accounts.ClientInfo) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status, topupID string
		err := tx.QueryRow(ctx, `SELECT status, topup_id FROM refund_requests WHERE id = $1 AND account_id = $2 FOR UPDATE`, id, u.AccountID).
			Scan(&status, &topupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != RequestOpen {
			return ErrRequestClosed
		}
		if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status = 'cancelled', resolved_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		_, err = s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorUser, ActorID: strconv.FormatInt(u.ID, 10),
			AccountID: u.AccountID, Action: accounts.ActionRefundCancelled, Client: c,
			Detail: map[string]any{"refund_request_id": id, "topup_id": topupID}})
		return err
	})
}

// OpenRefundRequests is `admin refund-request list`, oldest first.
func (s *Service) OpenRefundRequests(ctx context.Context) ([]CustomerRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT id, account_id, user_id, topup_id, amount_cents, reason, status, resolution, created_at, resolved_at
		FROM refund_requests WHERE status = 'open' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CustomerRequest, error) {
		var q CustomerRequest
		err := r.Scan(&q.ID, &q.AccountID, &q.UserID, &q.TopupID, &q.AmountCents, &q.Reason, &q.Status, &q.Resolution, &q.CreatedAt, &q.ResolvedAt)
		return q, err
	})
}

// DeclineRefundRequest closes an open request without refunding and emails
// the requester the operator's message before it returns. It reports
// whether the email went; one that did not stays queued in payment_emails.
func (s *Service) DeclineRefundRequest(ctx context.Context, id, message, reason, actor string) (bool, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return false, &ErrRefund{"a decline needs a message for the customer"}
	}
	if strings.TrimSpace(reason) == "" {
		return false, &ErrRefund{"a decline needs a reason"}
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var status, topupID string
		var accountID, userID int64
		err := tx.QueryRow(ctx, `SELECT status, account_id, user_id, topup_id FROM refund_requests WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &accountID, &userID, &topupID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &ErrRefund{"no refund request " + id}
		}
		if err != nil {
			return err
		}
		if status != RequestOpen {
			return &ErrRefund{"refund request " + id + " is already " + status}
		}
		if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status = 'declined', resolution = $2, resolved_at = $3 WHERE id = $1`,
			id, message, s.now()); err != nil {
			return err
		}
		if _, err := s.acc.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorAdmin, ActorID: actor, AccountID: accountID,
			Action: accounts.ActionRefundDeclined, Reason: reason,
			Detail: map[string]any{"refund_request_id": id, "topup_id": topupID, "message": message}}); err != nil {
			return err
		}
		var to string
		err = tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1 AND status = $2`, userID, accounts.UserActive).Scan(&to)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.queue(ctx, tx, id, mailRefundDeclined, to, accountID, mailer.RefundDecline{RequestID: id, TopupID: topupID, Message: message})
	})
	if err != nil {
		return false, err
	}
	if err := s.deliver(ctx, id); err != nil {
		return false, err
	}
	var unsent int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM payment_emails WHERE event_id = $1 AND sent_at IS NULL`, id).Scan(&unsent); err != nil {
		return false, err
	}
	var queued int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM payment_emails WHERE event_id = $1`, id).Scan(&queued); err != nil {
		return false, err
	}
	return queued > 0 && unsent == 0, nil
}
