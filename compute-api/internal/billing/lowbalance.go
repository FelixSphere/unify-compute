package billing

import (
	"context"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

// The low-balance warning (accounts.md, "Low-balance warning"; task W6).
// An account's available balance crossing its threshold downward is one
// event, detected in move under the account lock and reported once its
// transaction commits. Only movements made in the biller's own transactions
// (holds and extends) report one: AdjustTx's caller owns its transaction and
// its commit, so an operator debit leaves the warning armed and the next
// spend below the threshold reports it.

// LowBalanceCrossing is one downward crossing. EntryID is the balance entry
// that crossed, and names the occurrence: notify sends one email per id.
type LowBalanceCrossing struct {
	AccountID       int64
	EntryID         string
	Available, Held int64
	Threshold       int64
}

// creditKinds re-arm the warning when they leave available at or above the
// threshold. A released or expired hold does not: it is the end of a job,
// and re-arming there would mail an account near its threshold once per job.
var creditKinds = map[string]bool{"topup": true, "admin_credit": true}

// crossingTx carries a biller transaction's crossings to its commit.
type crossingTx struct {
	pgx.Tx
	out []LowBalanceCrossing
}

// OnLowBalance sets what is told of each crossing after its transaction
// commits. It must return quickly; LowBalanceMailer sends in the background.
func (l *Local) OnLowBalance(fn func(LowBalanceCrossing)) { l.lowBalance = fn }

// lowBalanceStep updates a.armed for movement e, already applied to a. It
// returns the transaction to record a crossing on, or nil for none.
func (l *Local) lowBalanceStep(tx pgx.Tx, a *account, e entry) *crossingTx {
	switch {
	case e.available > 0 && creditKinds[e.kind] && a.available >= a.lowBalance:
		a.armed = true
	case e.available < 0 && a.armed && a.lowBalance > 0 && a.available < a.lowBalance:
		ct, ok := tx.(*crossingTx)
		if !ok {
			return nil
		}
		a.armed = false
		return ct
	}
	return nil
}

func (l *Local) reportCrossings(cs []LowBalanceCrossing) {
	if l.lowBalance == nil {
		return
	}
	for _, c := range cs {
		l.lowBalance(c)
	}
}

// lowBalanceMailTimeout bounds one warning, owner lookup and SES included.
const lowBalanceMailTimeout = time.Minute

// LowBalanceMailer emails a crossing to the account's owners, once per
// crossing (notify, subject "balance"). Crossed returns at once: the hold or
// extend that crossed has committed, and neither SES's latency nor its
// failure may hold it up or fail it.
type LowBalanceMailer struct {
	notes *notify.Notifier
	mail  *mailer.Mailer
	log   *slog.Logger
	wg    sync.WaitGroup
}

func NewLowBalanceMailer(notes *notify.Notifier, mail *mailer.Mailer, log *slog.Logger) *LowBalanceMailer {
	if log == nil {
		log = slog.Default()
	}
	return &LowBalanceMailer{notes: notes, mail: mail, log: log}
}

func (m *LowBalanceMailer) Crossed(c LowBalanceCrossing) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				m.log.Error("low-balance email: panic; not sent", "account", c.AccountID, "entry", c.EntryID, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), lowBalanceMailTimeout)
		defer cancel()
		b := mailer.LowBalance{AvailableUSD: USD(c.Available), HeldUSD: USD(c.Held), ThresholdUSD: ThresholdUSD(c.Threshold)}
		_, _ = m.notes.ToOwners(ctx, notify.Key{AccountID: c.AccountID, Subject: "balance", SubjectID: c.EntryID, Message: mailer.KindLowBalance},
			func(ctx context.Context, to string) error { return m.mail.LowBalance(ctx, to, b) })
	}()
}

// Wait blocks until every warning started so far has been sent or given up
// on: for tests and for shutdown.
func (m *LowBalanceMailer) Wait() { m.wg.Wait() }

// ThresholdUSD renders a threshold with 2 decimals: it is set in cents.
func ThresholdUSD(q int64) string {
	return new(big.Rat).SetFrac64(q, sku.QuotaPerUSD).FloatString(2)
}
