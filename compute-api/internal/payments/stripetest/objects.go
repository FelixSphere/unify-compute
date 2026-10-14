package stripetest

// ChargeObject is a Charge as a charge.refunded event carries it:
// amount_refunded is cumulative over every refund of the charge.
func ChargeObject(paymentIntent string, amount, amountRefunded int64) map[string]any {
	return map[string]any{"id": "ch_for_" + paymentIntent, "object": "charge", "payment_intent": paymentIntent,
		"amount": amount, "amount_captured": amount, "amount_refunded": amountRefunded, "currency": "usd",
		"refunded": amountRefunded == amount, "status": "succeeded", "metadata": map[string]string{"product": "unify-compute"}}
}

// DisputeObject is a Dispute as charge.dispute.* events carry it. status is
// needs_response when opened, won or lost when closed (warning_* for an
// inquiry).
func DisputeObject(id, paymentIntent string, amount int64, status string) map[string]any {
	return map[string]any{"id": id, "object": "dispute", "charge": "ch_for_" + paymentIntent, "payment_intent": paymentIntent,
		"amount": amount, "currency": "usd", "status": status, "reason": "fraudulent", "is_charge_refundable": false}
}
