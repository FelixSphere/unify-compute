package jobs

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// mailTimeout bounds one job's outcome email, owner lookup and SES included.
const mailTimeout = time.Minute

// outcomeMessage is the email a terminal job gets (notifications.md,
// "Messages"), or "" for none: a customer's own cancel, a job that expired
// in the queue, and interrupted are not emailed.
func outcomeMessage(j store.Job) string {
	switch j.Status {
	case store.StatusSucceeded:
		return mailer.KindJobSucceeded
	case store.StatusFailed:
		return mailer.KindJobFailed
	case store.StatusCancelled:
		if j.Error != nil && (j.Error.Code == string(ledger.EndBalanceExhausted) || j.Error.Code == string(ledger.EndBillingUnavailable)) {
			return mailer.KindJobBillingStopped
		}
	}
	return ""
}

// accountOf is the account of a job owned under ACCOUNTS_BACKEND=local
// ("account:<id>", accounts.OwnerFor). A console-era owner has no address
// here and gets no email.
func accountOf(owner string) (int64, bool) {
	s, ok := strings.CutPrefix(owner, "account:")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

// jobEnded emails the account's owners about a job that just reached a
// terminal status. It returns at once: the email is sent in the background,
// so neither SES's latency nor its failure can hold up or fail the
// transition. notify makes it go out once however often this is called for
// the same job.
func (s *Service) jobEnded(j store.Job) {
	if s.mail == nil || s.notifier == nil || !j.Status.Terminal() {
		return
	}
	kind := outcomeMessage(j)
	account, ok := accountOf(j.Owner)
	if kind == "" || !ok {
		return
	}
	s.mailWG.Add(1)
	go func() {
		defer s.mailWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		defer cancel()
		o, err := s.outcome(ctx, j)
		if err != nil {
			s.log.Error("job outcome email: usage lookup; not sent", "job", j.ID, "err", err)
			return
		}
		send := map[string]notify.SendFunc{
			mailer.KindJobSucceeded:      func(ctx context.Context, to string) error { return s.mail.JobSucceeded(ctx, to, o) },
			mailer.KindJobFailed:         func(ctx context.Context, to string) error { return s.mail.JobFailed(ctx, to, o) },
			mailer.KindJobBillingStopped: func(ctx context.Context, to string) error { return s.mail.JobBillingStopped(ctx, to, o) },
		}[kind]
		_, _ = s.notifier.ToOwners(ctx, notify.Key{AccountID: account, Subject: "job", SubjectID: j.ID, Message: kind}, send)
	}()
}

// outcome reads what the email reports from the ledger: GPU-seconds, and the
// settled GPU charge, or what accrued while the final settle is pending.
func (s *Service) outcome(ctx context.Context, j store.Job) (mailer.JobOutcome, error) {
	o := mailer.JobOutcome{ID: j.ID, SKU: j.SKU, CostUSD: "0.0000"}
	if j.Error != nil {
		o.ErrorCode = j.Error.Code
	}
	if item, err := s.catalogue.Get(j.SKU); err == nil {
		o.GPU = item.GPU
	}
	u, err := s.Ledger.Usage(ctx, j.ID)
	if errors.Is(err, ledger.ErrUnknownJob) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	atts, err := s.Ledger.Attempts(ctx, j.ID)
	if err != nil {
		return o, err
	}
	o.Started = len(atts) > 0
	o.GPUSeconds = u.GPUSeconds
	q := u.SettledQuota
	if u.State != "closed" {
		q, o.CostPending = u.AccruedQuota, true
	}
	o.CostUSD = new(big.Rat).SetFrac64(q, sku.QuotaPerUSD).FloatString(4)
	return o, nil
}

// WaitMail blocks until every outcome email started so far has been sent or
// given up on: for tests, and for Run's return.
func (s *Service) WaitMail() { s.mailWG.Wait() }
