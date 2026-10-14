package mailer

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// The job outcome messages of notifications.md and the top-up receipt of
// accounts.md, "Emails". They are kept out of messages.go so that messages
// other tasks add there do not collide with these. Callers send them through
// internal/notify, which makes each go out once.

// Message kinds, also the notify.Key Message of each.
const (
	KindJobSucceeded      = "job-succeeded"
	KindJobFailed         = "job-failed"
	KindJobBillingStopped = "job-billing-stopped"
	KindTopupReceived     = "topup-received"
)

const (
	pathJobs    = "/jobs/"
	pathBilling = "/billing"

	reasonJob = "You're receiving this because a job on your Unify Compute account ended."
)

// JobOutcome is what a job outcome email says about the job.
type JobOutcome struct {
	ID  string
	SKU string
	// GPU is the SKU's GPU name (NVIDIA A10G); empty when the SKU has left
	// the catalogue.
	GPU        string
	GPUSeconds int64
	// CostUSD is the GPU charge, a decimal string with 4 places.
	CostUSD string
	// CostPending: the final settlement has not been made yet, so CostUSD
	// is what the ledger accrued and the email must not call it charged.
	CostPending bool
	// ErrorCode is the Job's error.code, as the API returns it.
	ErrorCode string
	// Started: the job reached UP, so GPU time was metered.
	Started bool
}

func (m *Mailer) jobURL(id string) string { return m.AppOrigin + pathJobs + url.PathEscape(id) }

func (j JobOutcome) gpu() string {
	if j.GPU == "" {
		return oneLine(j.SKU)
	}
	return oneLine(j.SKU) + " · " + oneLine(j.GPU)
}

func (j JobOutcome) details() []detail {
	cost := "Cost"
	if j.CostPending {
		cost = "Cost (pending settlement)"
	}
	d := []detail{
		{"Job", j.ID},
		{"GPU", j.gpu()},
		{"GPU-seconds", strconv.FormatInt(j.GPUSeconds, 10)},
		{cost, "USD " + j.CostUSD},
	}
	if j.ErrorCode != "" {
		d = append(d, detail{"Reason", oneLine(j.ErrorCode)})
	}
	return d
}

const unknownJob = "If you don't recognise this job, revoke the key that submitted it on the Keys page of the Unify Compute dashboard."

// JobSucceeded: the job's status became succeeded.
func (m *Mailer) JobSucceeded(ctx context.Context, to string, j JobOutcome) error {
	j.ErrorCode = ""
	pre := "Ran " + strconv.FormatInt(j.GPUSeconds, 10) + " GPU-seconds"
	if j.GPU != "" {
		pre += " on " + oneLine(j.GPU)
	}
	return m.send(ctx, to, &email{
		Kind:         KindJobSucceeded,
		Subject:      "Unify Compute: job " + j.ID + " finished",
		Preheader:    pre + ", for USD " + j.CostUSD + ".",
		Eyebrow:      "JOB FINISHED",
		Headline:     "Your job finished.",
		Intro:        j.ID + " exited successfully. Its page shows its events and any outputs it uploaded.",
		CTA:          &cta{Label: "View job", URL: m.jobURL(j.ID)},
		Status:       &status{"succeeded", "success"},
		Details:      j.details(),
		Safety:       "Cost is the GPU charge, taken from your prepaid balance. " + unknownJob,
		FooterReason: reasonJob,
	})
}

// failureLines says in plain words what each error.code means for the
// customer. The code itself is printed untranslated as the Reason row.
var failureLines = map[string]string{
	"capacity_exhausted":   "No provider had a GPU of this type free. Submitting it again later may work.",
	"image_pull_failed":    "The container image could not be pulled. Check the image name and tag, and that the image is public.",
	"provision_failed":     "The GPU machine could not be set up.",
	"nonzero_exit":         "Your job's command exited with an error.",
	"max_runtime_exceeded": "The job ran for its full max_runtime_seconds and was stopped.",
	"preempted":            "The provider took the machine back before the job could finish.",
	"provider_fault":       "The machine running the job stopped responding.",
}

// FailureLine is the plain-language sentence for an error.code.
func FailureLine(code string) string {
	if s, ok := failureLines[code]; ok {
		return s
	}
	return "The job did not finish. Its page has the full error message."
}

// JobFailed: the job's status became failed.
func (m *Mailer) JobFailed(ctx context.Context, to string, j JobOutcome) error {
	line := FailureLine(j.ErrorCode)
	pre, intro := line, ""
	if j.Started {
		intro = j.ID + " ended with an error after " + strconv.FormatInt(j.GPUSeconds, 10) + " GPU-seconds. " + line
	} else {
		pre += " No GPU time was charged."
		intro = j.ID + " could not start, so no GPU time was charged and its hold was released in full. " + line
	}
	if j.ErrorCode == "" {
		j.ErrorCode = "failed"
	}
	return m.send(ctx, to, &email{
		Kind:         KindJobFailed,
		Subject:      "Unify Compute: job " + j.ID + " failed",
		Preheader:    pre,
		Eyebrow:      "JOB FAILED",
		Headline:     "Your job failed.",
		Intro:        intro,
		CTA:          &cta{Label: "View job", URL: m.jobURL(j.ID)},
		Status:       &status{"failed", "danger"},
		Details:      j.details(),
		Safety:       "Reason is the job's error.code; the job page shows the full message. " + unknownJob,
		FooterReason: reasonJob,
	})
}

// JobBillingStopped: compute cancelled the job because its hold could not be
// extended (error.code balance_exhausted) or billing could not be reached
// for longer than the hold covered (billing_unavailable).
func (m *Mailer) JobBillingStopped(ctx context.Context, to string, j JobOutcome) error {
	e := &email{
		Kind:         KindJobBillingStopped,
		Eyebrow:      "JOB STOPPED",
		CTA:          &cta{Label: "View job", URL: m.jobURL(j.ID)},
		Status:       &status{"cancelled", "warning"},
		FooterReason: reasonJob,
	}
	if j.ErrorCode == "billing_unavailable" {
		e.Subject = "Unify Compute: job stopped, billing unavailable"
		e.Preheader = "We couldn't reach billing for " + j.ID + ", so we stopped it before it overspent its hold."
		e.Headline = "Your job was stopped: billing unavailable."
		e.Intro = "Billing could not be reached for long enough that " + j.ID + "'s hold was about to run out, so we stopped it. You pay only for the GPU-seconds it ran. This was an outage on our side, not a problem with your balance."
		e.Safety = "Nothing is needed from you. Resubmit when you're ready. " + unknownJob
		j.CostPending = true
	} else {
		j.ErrorCode = "balance_exhausted"
		e.Subject = "Unify Compute: job stopped, balance exhausted"
		e.Preheader = "Your balance could not cover more GPU time for " + j.ID + "."
		e.Headline = "Your job was stopped: balance exhausted."
		e.Intro = "The hold for " + j.ID + " could not be extended, so we stopped it before it ran past what your balance covers. You pay only for the GPU-seconds it ran."
		e.Safety = "Top up on the Billing page of the Unify Compute dashboard before you submit again. " + unknownJob
	}
	e.Details = j.details()
	return m.send(ctx, to, e)
}

// TopupReceipt is what the "Top-up received" email says.
type TopupReceipt struct {
	ID string // tu_…
	// AmountUSD has 2 places (the card charge), BalanceUSD 4 (the balance
	// is kept in quota units and jobs charge fractions of a cent).
	AmountUSD  string
	BalanceUSD string
	PaidAt     time.Time
	// ReceiptURL is Stripe's hosted receipt, when the charge lookup after
	// the credit found one.
	ReceiptURL string
}

// TopupReceived: a Stripe Checkout payment was credited to the balance.
func (m *Mailer) TopupReceived(ctx context.Context, to string, t TopupReceipt) error {
	c := &cta{Label: "View billing", URL: m.AppOrigin + pathBilling}
	if u, err := url.Parse(t.ReceiptURL); err == nil && u.Scheme == "https" && u.Host != "" {
		c = &cta{Label: "View receipt", URL: t.ReceiptURL}
	}
	return m.send(ctx, to, &email{
		Kind:      KindTopupReceived,
		Subject:   "Unify Compute: USD " + t.AmountUSD + " added to your balance",
		Preheader: "Your available balance is now USD " + t.BalanceUSD + ".",
		Eyebrow:   "TOP-UP",
		Headline:  "Your top-up was received.",
		Intro:     "USD " + t.AmountUSD + " was added to your Unify Compute balance. Jobs draw on it as they run.",
		CTA:       c,
		Details: []detail{
			{"Amount", "USD " + t.AmountUSD},
			{"Available balance", "USD " + t.BalanceUSD},
			{"Top-up", t.ID},
			{"Paid", utc(t.PaidAt)},
		},
		Safety:       "The card payment was processed by Stripe; the receipt is Stripe's and the balance history is on the Billing page. If you didn't make this payment, contact us at " + contactEmail + ".",
		FooterReason: reasonAccount,
	})
}
