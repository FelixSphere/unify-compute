package mailer

import "context"

// The low-balance warning (accounts.md, "Low-balance warning"; task W6),
// sent through internal/notify once per downward crossing of the account's
// threshold.

const KindLowBalance = "low-balance"

// LowBalance is what the warning says. AvailableUSD and HeldUSD have 4
// places, as balances do everywhere; ThresholdUSD 2, as it is set.
type LowBalance struct {
	AvailableUSD string
	HeldUSD      string
	ThresholdUSD string
}

// LowBalance: available fell below the account's low-balance threshold.
func (m *Mailer) LowBalance(ctx context.Context, to string, b LowBalance) error {
	return m.send(ctx, to, &email{
		Kind:      KindLowBalance,
		Subject:   "Unify Compute: your balance is below USD " + b.ThresholdUSD,
		Preheader: "Your available balance is USD " + b.AvailableUSD + ". Top up to keep submitting jobs.",
		Eyebrow:   "LOW BALANCE",
		Headline:  "Your balance is running low.",
		Intro: "Your available balance dropped below USD " + b.ThresholdUSD + ", the low-balance alert on your Unify Compute account. " +
			"A new job needs enough available balance for its hold, and a running job is stopped when its hold can't be extended.",
		CTA: &cta{Label: "Top up", URL: m.AppOrigin + pathBilling},
		Details: []detail{
			{"Available balance", "USD " + b.AvailableUSD},
			{"Held by running jobs", "USD " + b.HeldUSD},
			{"Alert below", "USD " + b.ThresholdUSD},
		},
		Safety: "We send this once each time your balance drops below the alert, and again only after a top-up brings it back to USD " + b.ThresholdUSD + " or more. " +
			"You can change the alert, or set it to 0 to turn it off, on the Billing page.",
		FooterReason: reasonAccount,
	})
}
