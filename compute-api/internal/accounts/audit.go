package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const (
	ActorUser   = "user"
	ActorAdmin  = "admin"
	ActorSystem = "system"
	ActorStripe = "stripe"
)

// Audit actions from spec/accounts.md "audit_events". Later tasks use the
// ones that belong to them (keys A5, money A6/B1/B2, closing B4).
const (
	ActionSignup            = "signup"
	ActionVerify            = "verify"
	ActionLogin             = "login"
	ActionLoginFailed       = "login_failed"
	ActionLogout            = "logout"
	ActionResetRequested    = "reset_requested"
	ActionPasswordReset     = "password_reset"
	ActionPasswordChanged   = "password_changed"
	ActionKeyCreated        = "key_created"
	ActionKeyRevoked        = "key_revoked"
	ActionTopupCreated      = "topup_created"
	ActionTopupCredited     = "topup_credited"
	ActionRefund            = "refund"
	ActionDisputeOpened     = "dispute_opened"
	ActionDisputeClosed     = "dispute_closed"
	ActionAccountSuspended  = "account_suspended"
	ActionAccountReinstated = "account_reinstated"
	ActionComputeEnabled    = "compute_enabled"
	ActionLimitsChanged     = "limits_changed"
	ActionAdminCredit       = "admin_credit"
	ActionAdminDebit        = "admin_debit"
	ActionExport            = "export"
	ActionAccountClosed     = "account_closed"
)

// ErrReasonRequired: every admin action carries a reason. The table refuses
// it too; checking here gives the CLI a plain error instead of a constraint
// name.
var ErrReasonRequired = errors.New("accounts: an admin action needs a reason")

type AuditEvent struct {
	ActorType string
	ActorID   string
	// AccountID is zero when no account is known (a failed login for an
	// unknown address).
	AccountID int64
	Action    string
	Client    ClientInfo
	Reason    string
	Detail    map[string]any
}

// Audit appends one row to audit_events and returns its id. Rows are never
// changed or removed; the table's trigger refuses UPDATE, DELETE and
// TRUNCATE.
func (s *Service) Audit(ctx context.Context, ev AuditEvent) (string, error) {
	return s.audit(ctx, s.db, ev)
}

// AuditTx is Audit inside the caller's transaction, for an action whose
// effect and audit row must commit together (an admin credit, a closing).
func (s *Service) AuditTx(ctx context.Context, tx pgx.Tx, ev AuditEvent) (string, error) {
	return s.audit(ctx, tx, ev)
}

func (s *Service) audit(ctx context.Context, db execer, ev AuditEvent) (string, error) {
	var reason *string
	if r := strings.TrimSpace(ev.Reason); r != "" {
		reason = &r
	} else if ev.ActorType == ActorAdmin {
		return "", ErrReasonRequired
	}
	detail := ev.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	var account *int64
	if ev.AccountID != 0 {
		account = &ev.AccountID
	}
	now := s.now()
	id := store.NewID("ae_", now)
	_, err = db.Exec(ctx, `INSERT INTO audit_events
		(id, at, actor_type, actor_id, account_id, action, ip, user_agent, reason, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		id, now, ev.ActorType, ev.ActorID, account, ev.Action, ev.Client.IP, ev.Client.UserAgent, reason, raw)
	if err != nil {
		return "", err
	}
	return id, nil
}
