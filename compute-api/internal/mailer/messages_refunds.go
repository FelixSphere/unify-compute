package mailer

import (
	"context"
	"strings"
	"time"
)

// The refund and dispute messages of spec/accounts.md, "Emails" (task B2):
// two to the account, and the operator's notices. The refund policy they
// state was decided by the operator on 2026-10-14: unused prepaid balance can
// be refunded on request; compute already used is not. The wording is
// pending legal review (ADR-0011 D6, plan O5).

const (
	// DefaultOperator is where operator notices go when OPERATOR_EMAIL is
	// unset: the contact address the site and every message already publish.
	DefaultOperator = contactEmail

	reasonOperator = "You're receiving this because this address is the Unify Compute operator address (OPERATOR_EMAIL)."

	refundPolicy = "Unused prepaid balance can be refunded on request; compute already used is not refunded."
)

// Refund is one refund applied to the balance. Amounts are USD strings as
// the page shows them ("20.00", "12.3400").
type Refund struct {
	TopupID        string `json:"topup_id"`
	AmountUSD      string `json:"amount_usd"`
	TopupAmountUSD string `json:"topup_amount_usd"`
	AvailableUSD   string `json:"available_usd"`
	// BonusReversedUSD is the share of the top-up's bonus credit the refund
	// took off the balance too (ADR-0013); empty when none.
	BonusReversedUSD string    `json:"bonus_reversed_usd,omitempty"`
	At               time.Time `json:"at"`
}

// RefundIssued tells the account that money went back to the card and came
// off the balance. Sent for every refund, whoever started it, once Stripe's
// charge.refunded has been applied.
func (m *Mailer) RefundIssued(ctx context.Context, to string, r Refund) error {
	took := "took the same amount off your balance"
	details := []detail{
		{"Refunded", "USD " + oneLine(r.AmountUSD)},
		{"Top-up", oneLine(r.TopupID) + " (USD " + oneLine(r.TopupAmountUSD) + ")"},
	}
	if r.BonusReversedUSD != "" {
		took = "took the same amount off your balance, with USD " + oneLine(r.BonusReversedUSD) + " of the bonus credit that came with it"
		details = append(details, detail{"Bonus credit removed", "USD " + oneLine(r.BonusReversedUSD)})
	}
	details = append(details, detail{"Available balance", "USD " + oneLine(r.AvailableUSD)}, detail{"Issued", utc(r.At)})
	return m.send(ctx, to, &email{
		Kind:      "refund-issued",
		Subject:   "Unify Compute: refund of USD " + oneLine(r.AmountUSD) + " issued",
		Preheader: "It usually reaches your card within 5 to 10 business days.",
		Eyebrow:   "REFUND",
		Headline:  "Your refund is on its way.",
		Intro: "We refunded USD " + oneLine(r.AmountUSD) + " of a Unify Compute top-up to the card that paid for it, and " + took + ". " +
			refundPolicy,
		CTA:          &cta{Label: "View balance", URL: m.AppOrigin + pathBilling},
		Details:      details,
		Safety:       "Your bank decides when the refund appears, usually within 5 to 10 business days. If you didn't ask for a refund, contact us at " + contactEmail + ".",
		FooterReason: reasonAccount,
	})
}

// Suspension is why an account was suspended.
type Suspension struct {
	TopupID   string    `json:"topup_id"`
	AmountUSD string    `json:"amount_usd"`
	At        time.Time `json:"at"`
}

// AccountSuspended tells the account it was suspended because a payment was
// disputed: what stopped, and that a person has to resolve it.
func (m *Mailer) AccountSuspended(ctx context.Context, to string, s Suspension) error {
	return m.send(ctx, to, &email{
		Kind:      "account-suspended",
		Subject:   "Your Unify Compute account is suspended",
		Preheader: "A card payment for your balance was disputed with your bank.",
		Eyebrow:   "ACCOUNT",
		Headline:  "Your account is suspended.",
		Intro: "Your bank told us that a payment for Unify Compute credit was disputed. While the dispute is open, the disputed amount is taken off your balance, " +
			"your running jobs are stopped, and new jobs and top-ups are refused. Usage up to the stop is charged.",
		CTA: &cta{Label: "Contact us", URL: "mailto:" + contactEmail},
		Details: []detail{
			{"Disputed", "USD " + oneLine(s.AmountUSD)},
			{"Top-up", oneLine(s.TopupID)},
			{"Suspended", utc(s.At)},
		},
		Safety:       "If you didn't mean to dispute this payment, contact us at " + contactEmail + " and tell your bank; if the dispute is resolved in our favour, the amount goes back on your balance.",
		FooterReason: reasonAccount,
	})
}

// OperatorNotice is a plain notice to the operator: what happened and what
// to do. Facts are label/value pairs, in order.
type OperatorNotice struct {
	Title  string      `json:"title"`
	Body   string      `json:"body"`
	Action string      `json:"action"`
	Facts  [][2]string `json:"facts"`
}

// Operator sends n to the operator address.
func (m *Mailer) Operator(ctx context.Context, to string, n OperatorNotice) error {
	var facts []detail
	for _, f := range n.Facts {
		facts = append(facts, detail{oneLine(f[0]), oneLine(f[1])})
	}
	title := oneLine(n.Title)
	return m.send(ctx, to, &email{
		Kind:         "operator-notice",
		Subject:      "[Unify Compute ops] " + title,
		Preheader:    strings.TrimSpace(oneLine(n.Action)),
		Eyebrow:      "OPERATOR",
		Headline:     title + ".",
		Intro:        n.Body,
		Details:      facts,
		Safety:       n.Action,
		FooterReason: reasonOperator,
	})
}

// RefundDecline is the operator's answer to a refund request they declined.
type RefundDecline struct {
	RequestID string `json:"request_id"`
	TopupID   string `json:"topup_id"`
	Message   string `json:"message"`
}

// RefundDeclined tells the customer their refund request was declined, with
// the operator's message (ADR-0013 §2).
func (m *Mailer) RefundDeclined(ctx context.Context, to string, d RefundDecline) error {
	return m.send(ctx, to, &email{
		Kind:         "refund-declined",
		Subject:      "About your Unify Compute refund request",
		Preheader:    "We couldn't refund this top-up.",
		Eyebrow:      "REFUND",
		Headline:     "We couldn't refund this top-up.",
		Intro:        oneLine(d.Message),
		CTA:          &cta{Label: "View billing", URL: m.AppOrigin + pathBilling},
		Details:      []detail{{"Request", oneLine(d.RequestID)}, {"Top-up", oneLine(d.TopupID)}},
		Safety:       "Your balance has not changed. Reply to " + contactEmail + " if you'd like to talk about it.",
		FooterReason: reasonAccount,
	})
}
