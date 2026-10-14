package admin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
)

// topup is `topup refund <tu_id> [--usd N] --reason TEXT` (task B2). It
// creates the refund at Stripe and changes nothing here: the balance is
// debited, the top-up updated and the customer emailed when Stripe's
// charge.refunded reaches the webhook, the same path a refund made in the
// Stripe dashboard takes. --amount is accepted as another name for --usd.
func (c *CLI) topup(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "refund" {
		return usage("topup refund <tu_id> [--usd AMOUNT] --reason TEXT")
	}
	if len(args) < 2 || strings.HasPrefix(args[1], "-") {
		return usage("missing top-up id")
	}
	id := args[1]
	if !strings.HasPrefix(id, "tu_") {
		return usage("top-up id must start with tu_, got %q", id)
	}
	var reason, usd string
	fs := flag.NewFlagSet("refund", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&reason, "reason", "", "")
	fs.StringVar(&usd, "usd", "", "")
	fs.StringVar(&usd, "amount", "", "")
	if err := fs.Parse(args[2:]); err != nil {
		return usage("%v", err)
	}
	if fs.NArg() > 0 {
		return usage("unexpected %q", fs.Arg(0))
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return usage("refund needs --reason")
	}
	cents := 0
	if usd != "" {
		var ok bool
		if cents, ok = payments.ParseAmount(usd); !ok || cents == 0 {
			return usage("--usd: %q is not a USD amount with at most 2 decimals", usd)
		}
	}
	if c.Payments == nil {
		return errors.New("top-up refunds need ACCOUNTS_BACKEND=local and the Stripe keys")
	}
	r, err := c.Payments.RequestRefund(ctx, payments.RefundRequest{TopupID: id, Cents: cents, Actor: c.Actor, Reason: reason})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "top-up %s: refund %s of USD %s created at Stripe (%s), account %d\n", id, r.RefundID,
		payments.CentsUSD(r.Cents), r.Status, r.AccountID)
	fmt.Fprintln(c.Out, "  the balance is debited and the customer emailed when Stripe's charge.refunded reaches the webhook")
	return nil
}
