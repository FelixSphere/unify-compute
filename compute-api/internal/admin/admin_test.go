package admin_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

var ctx = context.Background()

type mailbox struct {
	mu   sync.Mutex
	sent []string
	fail error
}

func (m *mailbox) ComputeEnabled(_ context.Context, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, to)
	return nil
}

type env struct {
	t    *testing.T
	db   *pgxpool.Pool
	acc  *accounts.Service
	bill *billing.Local
	mail *mailbox
	out  bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	return &env{t: t, db: st.Pool(), acc: accounts.New(st.Pool(), accounts.Config{}), bill: billing.New(st.Pool(), nil), mail: &mailbox{}}
}

func (e *env) run(args ...string) error {
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Mailer: e.mail, Actor: "ops-alice", Out: &e.out}
	return cli.Run(ctx, args)
}

// signup creates an account with one user, verified unless pending.
func (e *env) signup(email string, verified bool) int64 {
	e.t.Helper()
	u, a, err := e.acc.CreatePending(ctx, email, "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA")
	if err != nil {
		e.t.Fatal(err)
	}
	if verified {
		if _, err := e.db.Exec(ctx, `UPDATE users SET status = 'active', email_verified_at = now() WHERE id = $1`, u.ID); err != nil {
			e.t.Fatal(err)
		}
	}
	return a.ID
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

type auditRow struct {
	ID, ActorType, ActorID, Action, Reason string
	AccountID                              int64
	Detail                                 map[string]any
}

func (e *env) audits(account int64) []auditRow {
	e.t.Helper()
	rows, err := e.db.Query(ctx, `SELECT id, actor_type, actor_id, action, coalesce(reason, ''), account_id, detail
		FROM audit_events WHERE account_id = $1 ORDER BY seq`, account)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.ID, &r.ActorType, &r.ActorID, &r.Action, &r.Reason, &r.AccountID, &r.Detail); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

func TestBalanceCreditWritesAnEntryAndAnAuditEventWithItsReason(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if err := e.run("balance", "credit", id(acct), "--usd", "5", "--reason", "A8 staging slice funding"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "credited USD 5.0000; available USD 5.0000, held USD 0.0000") {
		t.Errorf("output %q", e.out.String())
	}
	b, err := e.bill.Balance(ctx, acct)
	if err != nil || b.Available != 5*500_000 || b.Held != 0 {
		t.Fatalf("balance %+v %v", b, err)
	}
	au := e.audits(acct)
	if len(au) != 1 || au[0].Action != accounts.ActionAdminCredit || au[0].ActorType != accounts.ActorAdmin ||
		au[0].ActorID != "ops-alice" || au[0].Reason != "A8 staging slice funding" || au[0].Detail["usd"] != "5.0000" {
		t.Fatalf("audit %+v", au)
	}
	es, err := e.bill.Entries(ctx, acct)
	if err != nil || len(es) != 1 {
		t.Fatalf("entries %+v %v", es, err)
	}
	if en := es[0]; en.Kind != "admin_credit" || en.AvailableDelta != 2_500_000 || en.AvailableAfter != 2_500_000 ||
		en.RefType != "admin" || en.RefID != au[0].ID || en.IdempotencyKey != "admin:"+au[0].ID {
		t.Fatalf("entry %+v, audit id %s", en, au[0].ID)
	}
	if !strings.Contains(e.out.String(), au[0].ID) {
		t.Errorf("output does not name the audit event: %q", e.out.String())
	}
	if err := e.run("audit"); err != nil {
		t.Fatalf("audit after credit: %v (%s)", err, e.out.String())
	}
}

func TestBalanceCreditRefusesWithoutAReasonAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	for _, args := range [][]string{
		{"balance", "credit", id(acct), "--usd", "5"},
		{"balance", "credit", id(acct), "--usd", "5", "--reason", "   "},
		{"balance", "debit", id(acct), "--usd", "5"},
		{"account", "enable-compute", id(acct)},
		{"account", "suspend", id(acct), "--reason="},
	} {
		if err := e.run(args...); !errors.Is(err, admin.ErrUsage) || !strings.Contains(err.Error(), "--reason") {
			t.Errorf("%v: want a usage error naming --reason, got %v", args, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM balance_entries`); n != 0 {
		t.Errorf("%d balance entries written", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events`); n != 0 {
		t.Errorf("%d audit events written", n)
	}
	a, _ := e.acc.AccountByID(ctx, acct)
	if a.Available != 0 || a.ComputeEnabled || a.Status != accounts.AccountActive {
		t.Errorf("account changed: %+v", a)
	}
}

func TestBalanceCreditRefusesAnAccountNobodyVerified(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pending@example.com", false)
	err := e.run("balance", "credit", id(acct), "--usd", "5", "--reason", "x")
	if err == nil || !strings.Contains(err.Error(), "no verified user") {
		t.Fatalf("want refusal, got %v", err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events`); n != 0 {
		t.Errorf("refused credit left %d audit events", n)
	}
}

func TestBalanceDebitNeverGoesBelowZero(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if err := e.run("balance", "credit", id(acct), "--usd", "2.50", "--reason", "fund"); err != nil {
		t.Fatal(err)
	}
	if err := e.run("balance", "debit", id(acct), "--usd", "2.5001", "--reason", "too much"); !errors.Is(err, billing.ErrInsufficientFunds) {
		t.Fatalf("over-debit: %v", err)
	}
	if err := e.run("balance", "debit", id(acct), "--usd", "1", "--reason", "mistaken credit"); err != nil {
		t.Fatal(err)
	}
	if err := e.run("balance", "show", id(acct)); err != nil || !strings.Contains(e.out.String(), "available USD 1.5000, held USD 0.0000") {
		t.Fatalf("show: %v %q", err, e.out.String())
	}
	au := e.audits(acct)
	if len(au) != 2 || au[1].Action != accounts.ActionAdminDebit || au[1].Reason != "mistaken credit" {
		t.Fatalf("audit %+v", au)
	}
	if err := e.run("balance", "credit", "999", "--usd", "1", "--reason", "x"); err == nil {
		t.Fatal("credit to an unknown account succeeded")
	}
}

// ADR-0011 D3: compute is off for a new account, turned on by hand, and
// turning it on tells the customer.
func TestEnableComputeFlipsTheSwitchAuditsAndEmails(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if a, _ := e.acc.AccountByID(ctx, acct); a.ComputeEnabled {
		t.Fatal("a new account must start with compute off (D3)")
	}
	if err := e.bill.CheckSubmit(ctx, int(acct)); !errors.Is(err, billing.ErrComputeNotEnabled) {
		t.Fatalf("submit gate before: %v", err)
	}
	if err := e.run("account", "enable-compute", id(acct), "--reason", "pilot customer"); err != nil {
		t.Fatal(err)
	}
	if err := e.bill.CheckSubmit(ctx, int(acct)); err != nil {
		t.Fatalf("submit gate after: %v", err)
	}
	if len(e.mail.sent) != 1 || e.mail.sent[0] != "pat@example.com" {
		t.Fatalf("mail %v", e.mail.sent)
	}
	au := e.audits(acct)
	if len(au) != 1 || au[0].Action != accounts.ActionComputeEnabled || au[0].Detail["compute_enabled"] != true || au[0].Reason != "pilot customer" {
		t.Fatalf("audit %+v", au)
	}
	// Again: nothing changes, nothing is audited, nobody is emailed twice.
	if err := e.run("account", "enable-compute", id(acct), "--reason", "again"); err != nil || !strings.Contains(e.out.String(), "already enabled") {
		t.Fatalf("repeat: %v %q", err, e.out.String())
	}
	if len(e.audits(acct)) != 1 || len(e.mail.sent) != 1 {
		t.Fatal("a repeated enable wrote or sent something")
	}
	if err := e.run("account", "disable-compute", id(acct), "--reason", "paused"); err != nil {
		t.Fatal(err)
	}
	if err := e.bill.CheckSubmit(ctx, int(acct)); !errors.Is(err, billing.ErrComputeNotEnabled) {
		t.Fatalf("submit gate after disable: %v", err)
	}
	if au := e.audits(acct); len(au) != 2 || au[1].Detail["compute_enabled"] != false || len(e.mail.sent) != 1 {
		t.Fatalf("disable: audit %+v mail %v", au, e.mail.sent)
	}
}

func TestEnableComputeReportsAFailedEmailWithoutUndoingTheSwitch(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	e.mail.fail = errors.New("ses: throttled")
	err := e.run("account", "enable-compute", id(acct), "--reason", "pilot")
	if !errors.Is(err, admin.ErrNotNotified) || !strings.Contains(err.Error(), "pat@example.com") {
		t.Fatalf("want ErrNotNotified, got %v", err)
	}
	if a, _ := e.acc.AccountByID(ctx, acct); !a.ComputeEnabled {
		t.Fatal("a failed email undid the switch")
	}
}

func TestEnableComputeWithoutAMailerSaysNoEmailWasSent(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Actor: "ops", Out: &e.out}
	if err := cli.Run(ctx, []string{"account", "enable-compute", id(acct), "--reason", "pilot"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "no email sent") {
		t.Errorf("output %q", e.out.String())
	}
}

func TestSuspendAndReinstate(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if err := e.run("account", "enable-compute", id(acct), "--reason", "pilot"); err != nil {
		t.Fatal(err)
	}
	if err := e.run("account", "suspend", id(acct), "--reason", "abuse report 17"); err != nil {
		t.Fatal(err)
	}
	a, _ := e.acc.AccountByID(ctx, acct)
	if a.Status != accounts.AccountSuspended || a.SuspendedReason != "operator" {
		t.Fatalf("account %+v", a)
	}
	if err := e.bill.CheckSubmit(ctx, int(acct)); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("gate: %v", err)
	}
	if err := e.run("account", "show", "PAT@example.com "); err != nil || !strings.Contains(e.out.String(), "status: suspended (operator)") {
		t.Fatalf("show by email: %v %q", err, e.out.String())
	}
	if err := e.run("account", "reinstate", id(acct), "--reason", "resolved"); err != nil {
		t.Fatal(err)
	}
	if err := e.bill.CheckSubmit(ctx, int(acct)); err != nil {
		t.Fatalf("gate after reinstate: %v", err)
	}
	actions := []string{}
	for _, r := range e.audits(acct) {
		actions = append(actions, r.Action)
	}
	if got := strings.Join(actions, ","); got != "compute_enabled,account_suspended,account_reinstated" {
		t.Fatalf("audit actions %s", got)
	}
}

func TestAccountShow(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if err := e.run("account", "show", id(acct)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"account " + id(acct) + " (account:" + id(acct) + ")", "status: active", "compute_enabled: false",
		"available: USD 0.0000", "users: pat@example.com (active)"} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("show lacks %q:\n%s", want, e.out.String())
		}
	}
	if err := e.run("account", "show", "nobody@example.com"); err == nil {
		t.Error("unknown email")
	}
}

func TestAuditFindsABalanceThatDisagreesWithItsEntries(t *testing.T) {
	e := newEnv(t)
	acct := e.signup("pat@example.com", true)
	if err := e.run("balance", "credit", id(acct), "--usd", "1", "--reason", "fund"); err != nil {
		t.Fatal(err)
	}
	if err := e.run("audit", "--account", id(acct)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE accounts SET available = available + 1 WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if err := e.run("audit"); !errors.Is(err, admin.ErrAuditFailed) || !strings.Contains(e.out.String(), "account "+id(acct)) {
		t.Fatalf("want ErrAuditFailed naming the account, got %v %q", err, e.out.String())
	}
}

func TestUsageErrorsTouchNothing(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{},
		{"refund"},
		{"account"},
		{"account", "delete", "1", "--reason", "x"},
		{"account", "enable-compute"},
		{"account", "enable-compute", "--reason", "x"},
		{"account", "enable-compute", "abc", "--reason", "x"},
		{"account", "enable-compute", "1", "--reason", "x", "extra"},
		{"balance", "credit", "1", "--reason", "x"},
		{"balance", "credit", "1", "--usd", "1.23456", "--reason", "x"},
		{"balance", "credit", "1", "--usd", "-1", "--reason", "x"},
		{"balance", "credit", "1", "--usd", "0", "--reason", "x"},
		{"audit", "1"},
	} {
		if err := e.run(args...); !errors.Is(err, admin.ErrUsage) {
			t.Errorf("%v: want a usage error, got %v", args, err)
		}
	}
}

func TestParseUSD(t *testing.T) {
	for in, want := range map[string]int64{"12.50": 6_250_000, "0.0001": 50, "1": 500_000, "100.00": 50_000_000, "0.01": 5000} {
		if got, err := admin.ParseUSD(in); err != nil || got != want {
			t.Errorf("ParseUSD(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0.0000", "-1", "1.23456", "1e3", ".5", "1.", "1,5", " 1", "12345678"} {
		if _, err := admin.ParseUSD(in); err == nil {
			t.Errorf("ParseUSD(%q) accepted", in)
		}
	}
}
