package admin_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// `admin refund-request list|decline` (ADR-0013 §2). Creating a request and
// its refund are covered end to end in internal/dashboard.

func (e *refundEnv) requests(args ...string) error {
	e.out.Reset()
	cli := &admin.CLI{DB: e.db, Accounts: e.acc, Biller: e.bill, Payments: e.pay, Actor: "ops-alice", Out: &e.out}
	return cli.Run(ctx, append([]string{"refund-request"}, args...))
}

func TestRefundRequestListAndDecline(t *testing.T) {
	e := newRefundEnv(t)
	acct := e.signup("ada@example.com", true)
	id := e.paid(acct, 2000, "pi_rr")
	if err := e.requests("list"); err != nil || !strings.Contains(e.out.String(), "no open refund requests") {
		t.Fatalf("empty list: %v %q", err, e.out.String())
	}
	rr := store.NewID("rr_", time.Now())
	if _, err := e.db.Exec(ctx, `INSERT INTO refund_requests (id, account_id, user_id, topup_id, amount_cents, reason, created_at)
		VALUES ($1, $2, 1, $3, 2000, 'done early', now())`, rr, acct, id); err != nil {
		t.Fatal(err)
	}
	if err := e.requests("list"); err != nil || !strings.Contains(e.out.String(), rr+"  account ") || !strings.Contains(e.out.String(), "USD 20.00") {
		t.Fatalf("list: %v %q", err, e.out.String())
	}
	for _, args := range [][]string{
		{"decline", rr, "--reason", "spent"},          // no message
		{"decline", rr, "--message", "Used by jobs."}, // no reason
		{"decline", "tu_1", "--message", "m", "--reason", "r"},
		{"decline"},
		{"approve", rr},
	} {
		if err := e.requests(args...); !errors.Is(err, admin.ErrUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	// No mailer here: the decline commits and says the email did not go.
	if err := e.requests("decline", rr, "--message", "Used by jobs.", "--reason", "spent"); !errors.Is(err, admin.ErrNotNotified) {
		t.Fatalf("decline: %v", err)
	}
	var status, resolution string
	if err := e.db.QueryRow(ctx, `SELECT status, resolution FROM refund_requests WHERE id = $1`, rr).Scan(&status, &resolution); err != nil ||
		status != "declined" || resolution != "Used by jobs." {
		t.Fatalf("request %s %q %v", status, resolution, err)
	}
	if err := e.requests("decline", rr, "--message", "m", "--reason", "r"); err == nil || !strings.Contains(err.Error(), "already declined") {
		t.Fatalf("second decline: %v", err)
	}
}
