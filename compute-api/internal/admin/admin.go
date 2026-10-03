// Package admin is `compute-api admin`, the operator's CLI on the host
// (docs/spec/accounts.md, "Admin CLI"). It is the only admin surface: there
// is no web admin. Every mutating verb requires --reason and writes its
// change and an audit_events row in one transaction.
//
// It runs with the service's environment (DATABASE_URL) and never migrates:
// the service does that at boot, and a CLI that migrated could apply a newer
// schema under a running older service.
package admin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

const Usage = `usage: compute-api admin <command>

  account show <account_id|email>
  account enable-compute <account_id> --reason TEXT
  account disable-compute <account_id> --reason TEXT
  account suspend <account_id> --reason TEXT
  account reinstate <account_id> --reason TEXT
  balance show <account_id>
  balance credit <account_id> --usd AMOUNT --reason TEXT
  balance debit <account_id> --usd AMOUNT --reason TEXT
  audit [--account <account_id>]

AMOUNT is USD with at most 4 decimals, e.g. 12.50.`

// ErrUsage is a command line the CLI does not accept; nothing was read or
// written. main prints Usage and exits 2.
var ErrUsage = errors.New("usage")

// ErrAuditFailed means audit found an account whose balance differs from its
// entries. main exits 1.
var ErrAuditFailed = errors.New("admin: balance invariant broken")

// ErrNotNotified means compute was enabled and committed, but the email
// telling the customer was not sent. main exits 1 so a script notices.
var ErrNotNotified = errors.New("compute is enabled, but the notification email was not sent")

// Mailer is the one message the CLI sends (internal/mailer).
type Mailer interface {
	ComputeEnabled(ctx context.Context, to string) error
}

type CLI struct {
	DB       *pgxpool.Pool
	Accounts *accounts.Service
	Biller   *billing.Local
	// Mailer is nil when the deployment sends no account mail; then
	// enable-compute says so and changes nothing else.
	Mailer Mailer
	// Actor is who ran the command, recorded as the audit event's actor_id.
	Actor string
	Out   io.Writer
}

func usage(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}

// Run executes one command. args excludes "admin" itself.
func (c *CLI) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("no command")
	}
	switch args[0] {
	case "account":
		return c.account(ctx, args[1:])
	case "balance":
		return c.balance(ctx, args[1:])
	case "audit":
		return c.audit(ctx, args[1:])
	}
	return usage("unknown command %q", args[0])
}

// verb splits "<verb> <account_id> [flags]": the id comes before the flags,
// as the spec writes the commands, which the flag package alone would not
// accept.
type verb struct {
	name, target, reason, usd string
}

func parse(args []string, needUSD bool) (*verb, error) {
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return nil, usage("missing account")
	}
	v := &verb{name: args[0], target: args[1]}
	fs := flag.NewFlagSet(v.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&v.reason, "reason", "", "")
	if needUSD {
		fs.StringVar(&v.usd, "usd", "", "")
	}
	if err := fs.Parse(args[2:]); err != nil {
		return nil, usage("%v", err)
	}
	if fs.NArg() > 0 {
		return nil, usage("unexpected %q", fs.Arg(0))
	}
	return v, nil
}

func (v *verb) accountID() (int64, error) {
	id, err := strconv.ParseInt(v.target, 10, 64)
	if err != nil || id <= 0 {
		return 0, usage("account must be a numeric id, got %q", v.target)
	}
	return id, nil
}

func (v *verb) requireReason() (string, error) {
	r := strings.TrimSpace(v.reason)
	if r == "" {
		return "", usage("%s needs --reason", v.name)
	}
	return r, nil
}

func (c *CLI) account(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("account needs a verb")
	}
	switch args[0] {
	case "show":
		if len(args) != 2 {
			return usage("account show <account_id|email>")
		}
		return c.show(ctx, args[1])
	case "enable-compute", "disable-compute", "suspend", "reinstate":
	default:
		return usage("unknown account verb %q", args[0])
	}
	v, err := parse(args, false)
	if err != nil {
		return err
	}
	id, err := v.accountID()
	if err != nil {
		return err
	}
	reason, err := v.requireReason()
	if err != nil {
		return err
	}
	switch v.name {
	case "enable-compute", "disable-compute":
		return c.setCompute(ctx, id, v.name == "enable-compute", reason)
	default:
		return c.setStatus(ctx, id, v.name == "suspend", reason)
	}
}

// auditEvent writes the audit event for an admin action in tx and returns
// its id.
func (c *CLI) auditEvent(ctx context.Context, tx pgx.Tx, accountID int64, action, reason string, detail map[string]any) (string, error) {
	id, err := c.Accounts.AuditTx(ctx, tx, accounts.AuditEvent{ActorType: accounts.ActorAdmin, ActorID: c.Actor,
		AccountID: accountID, Action: action, Reason: reason, Detail: detail})
	if err != nil {
		return "", fmt.Errorf("admin: audit event: %w", err)
	}
	return id, nil
}

func (c *CLI) lockAccount(ctx context.Context, tx pgx.Tx, id int64) (status string, enabled bool, err error) {
	err = tx.QueryRow(ctx, `SELECT status, compute_enabled FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&status, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("account %d: %w", id, billing.ErrUnknownAccount)
	}
	return status, enabled, err
}

// setCompute is ADR-0011 D3's switch: compute_enabled is off for a new
// account and turned on by hand, per account. Turning it on emails the
// account's verified owner, after the change has committed: an email that
// fails never undoes the switch, and it is reported so it can be resent by
// hand.
func (c *CLI) setCompute(ctx context.Context, id int64, on bool, reason string) error {
	changed := false
	err := pgx.BeginFunc(ctx, c.DB, func(tx pgx.Tx) error {
		_, enabled, err := c.lockAccount(ctx, tx, id)
		if err != nil || enabled == on {
			return err
		}
		changed = true
		if _, err := tx.Exec(ctx, `UPDATE accounts SET compute_enabled = $2 WHERE id = $1`, id, on); err != nil {
			return err
		}
		_, err = c.auditEvent(ctx, tx, id, accounts.ActionComputeEnabled, reason, map[string]any{"compute_enabled": on})
		return err
	})
	if err != nil {
		return err
	}
	state := map[bool]string{true: "enabled", false: "disabled"}[on]
	if !changed {
		fmt.Fprintf(c.Out, "account %d: compute already %s; nothing changed\n", id, state)
		return nil
	}
	fmt.Fprintf(c.Out, "account %d: compute %s\n", id, state)
	if !on {
		return nil
	}
	rows, err := c.DB.Query(ctx, `SELECT email FROM users WHERE account_id = $1 AND status = $2 ORDER BY id`, id, accounts.UserActive)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotNotified, err)
	}
	to, err := pgx.CollectRows(rows, pgx.RowTo[string])
	switch {
	case err != nil:
		return fmt.Errorf("%w: %v", ErrNotNotified, err)
	case c.Mailer == nil:
		fmt.Fprintln(c.Out, "  no email sent: this deployment has no mailer (SES_SENDER unset)")
		return nil
	case len(to) == 0:
		fmt.Fprintln(c.Out, "  no email sent: the account has no verified user")
		return nil
	}
	for _, addr := range to {
		if err := c.Mailer.ComputeEnabled(ctx, addr); err != nil {
			return fmt.Errorf("%w to %s: %v", ErrNotNotified, addr, err)
		}
		fmt.Fprintf(c.Out, "  emailed %s\n", addr)
	}
	return nil
}

// setStatus suspends or reinstates. It does not stop running jobs: a
// suspended account cannot submit, and cancelling on suspension comes with
// disputes (task B2).
func (c *CLI) setStatus(ctx context.Context, id int64, suspend bool, reason string) error {
	from, to, action := accounts.AccountActive, accounts.AccountSuspended, accounts.ActionAccountSuspended
	if !suspend {
		from, to, action = accounts.AccountSuspended, accounts.AccountActive, accounts.ActionAccountReinstated
	}
	changed := false
	err := pgx.BeginFunc(ctx, c.DB, func(tx pgx.Tx) error {
		status, _, err := c.lockAccount(ctx, tx, id)
		switch {
		case err != nil:
			return err
		case status == to:
			return nil
		case status != from:
			return fmt.Errorf("account %d is %s, not %s; nothing changed", id, status, from)
		}
		if !suspend {
			var available int64
			if err := tx.QueryRow(ctx, `SELECT available FROM accounts WHERE id = $1`, id).Scan(&available); err != nil {
				return err
			}
			if available < 0 {
				return fmt.Errorf("account %d has a negative balance (USD %s); credit it before reinstating", id, billing.USD(available))
			}
		}
		changed = true
		var why *string
		if suspend {
			op := "operator"
			why = &op
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = $2, suspended_reason = $3 WHERE id = $1`, id, to, why); err != nil {
			return err
		}
		_, err = c.auditEvent(ctx, tx, id, action, reason, map[string]any{"from": from, "to": to})
		return err
	})
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(c.Out, "account %d: already %s; nothing changed\n", id, to)
		return nil
	}
	fmt.Fprintf(c.Out, "account %d: %s\n", id, to)
	return nil
}

func (c *CLI) show(ctx context.Context, target string) error {
	var id int64
	if n, err := strconv.ParseInt(target, 10, 64); err == nil && n > 0 {
		id = n
	} else {
		u, err := c.Accounts.UserByEmail(ctx, target)
		if errors.Is(err, accounts.ErrNotFound) {
			return fmt.Errorf("no user with email %q", target)
		}
		if err != nil {
			return err
		}
		id = u.AccountID
	}
	a, err := c.Accounts.AccountByID(ctx, id)
	if errors.Is(err, accounts.ErrNotFound) {
		return fmt.Errorf("account %d: %w", id, billing.ErrUnknownAccount)
	}
	if err != nil {
		return err
	}
	rows, err := c.DB.Query(ctx, `SELECT email || ' (' || status || ')' FROM users WHERE account_id = $1 ORDER BY id`, id)
	if err != nil {
		return err
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	status := a.Status
	if a.SuspendedReason != "" {
		status += " (" + a.SuspendedReason + ")"
	}
	fmt.Fprintf(c.Out, "account %d (%s)\n  status: %s\n  compute_enabled: %t\n  available: USD %s\n  held: USD %s\n  created: %s\n  users: %s\n",
		a.ID, a.Owner(), status, a.ComputeEnabled, billing.USD(a.Available), billing.USD(a.Held),
		a.CreatedAt.UTC().Format(time.RFC3339), strings.Join(users, ", "))
	return nil
}

func (c *CLI) balance(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("balance needs a verb")
	}
	switch args[0] {
	case "show":
		v, err := parse(args, false)
		if err != nil {
			return err
		}
		id, err := v.accountID()
		if err != nil {
			return err
		}
		b, err := c.Biller.Balance(ctx, id)
		if err != nil {
			return fmt.Errorf("account %d: %w", id, err)
		}
		fmt.Fprintf(c.Out, "account %d: available USD %s, held USD %s\n", id, billing.USD(b.Available), billing.USD(b.Held))
		return nil
	case "credit", "debit":
	default:
		return usage("unknown balance verb %q", args[0])
	}
	v, err := parse(args, true)
	if err != nil {
		return err
	}
	id, err := v.accountID()
	if err != nil {
		return err
	}
	reason, err := v.requireReason()
	if err != nil {
		return err
	}
	q, err := ParseUSD(v.usd)
	if err != nil {
		return usage("--usd: %v", err)
	}
	action := accounts.ActionAdminCredit
	if v.name == "debit" {
		q, action = -q, accounts.ActionAdminDebit
	}
	// An account nobody has verified is deleted after 7 days
	// (accounts.PurgeUnverified), and its money with it: credit only one a
	// person has proven an address for.
	var verified bool
	if err := c.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE account_id = $1 AND status = $2)`,
		id, accounts.UserActive).Scan(&verified); err != nil {
		return err
	}
	if !verified && q > 0 {
		return fmt.Errorf("account %d has no verified user; it would be purged with its balance. Verify first", id)
	}
	// The audit row first, so the balance entry can name it: ref admin/ae_…
	// and idempotency key admin:ae_…, one entry per audited action.
	var auditID string
	var after billing.Balance
	err = pgx.BeginFunc(ctx, c.DB, func(tx pgx.Tx) error {
		var err error
		auditID, err = c.auditEvent(ctx, tx, id, action, reason, map[string]any{"usd": billing.USD(abs(q)), "quota": abs(q)})
		if err != nil {
			return err
		}
		after, err = c.Biller.AdjustTx(ctx, tx, billing.Adjustment{AccountID: id, Quota: q, RefType: "admin", RefID: auditID,
			Detail: map[string]string{"reason": reason}})
		return err
	})
	if err != nil {
		return fmt.Errorf("account %d: %w", id, err)
	}
	fmt.Fprintf(c.Out, "account %d: %s USD %s; available USD %s, held USD %s (audit %s)\n",
		id, map[string]string{"admin_credit": "credited", "admin_debit": "debited"}[action], billing.USD(abs(q)),
		billing.USD(after.Available), billing.USD(after.Held), auditID)
	return nil
}

func abs(q int64) int64 {
	if q < 0 {
		return -q
	}
	return q
}

func (c *CLI) audit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	account := fs.Int64("account", 0, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return usage("audit [--account <account_id>]")
	}
	var only *int64
	if *account > 0 {
		only = account
	}
	bad, err := c.Biller.Audit(ctx, only)
	if err != nil {
		return err
	}
	for _, m := range bad {
		fmt.Fprintf(c.Out, "account %d: available %d, held %d; entries sum to %d, %d\n", m.AccountID, m.Available, m.Held, m.SumAvailable, m.SumHeld)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w on %d account(s)", ErrAuditFailed, len(bad))
	}
	fmt.Fprintln(c.Out, "balance invariant holds: every account's entries sum to its balance")
	return nil
}

var usdPattern = regexp.MustCompile(`^([0-9]{1,7})(?:\.([0-9]{1,4}))?$`)

// ParseUSD turns a positive USD amount with at most 4 decimals into quota
// units, exactly: 0.0001 USD is 50 units.
func ParseUSD(s string) (int64, error) {
	m := usdPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a USD amount with at most 4 decimals", s)
	}
	whole, _ := strconv.ParseInt(m[1], 10, 64)
	frac, _ := strconv.ParseInt((m[2] + "0000")[:4], 10, 64)
	q := whole*sku.QuotaPerUSD + frac*(sku.QuotaPerUSD/10000)
	if q <= 0 {
		return 0, errors.New("amount must be positive")
	}
	return q, nil
}
