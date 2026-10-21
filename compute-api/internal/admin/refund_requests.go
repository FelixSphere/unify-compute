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

// refundRequest is `refund-request list` and `refund-request decline <rr_id>
// --message TEXT --reason TEXT` (ADR-0013 §2). A request is refunded with
// `topup refund`, whose applied refund closes it.
func (c *CLI) refundRequest(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("refund-request list | decline <rr_id> --message TEXT --reason TEXT")
	}
	if c.Payments == nil {
		return errors.New("refund requests need ACCOUNTS_BACKEND=local")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usage("refund-request list")
		}
		list, err := c.Payments.OpenRefundRequests(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(c.Out, "no open refund requests")
			return nil
		}
		for _, r := range list {
			fmt.Fprintf(c.Out, "%s  account %d  %s  USD %s  %s  %q\n", r.ID, r.AccountID, r.TopupID, payments.CentsUSD(r.AmountCents),
				r.CreatedAt.UTC().Format("2006-01-02 15:04Z"), r.Reason)
		}
		return nil
	case "decline":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usage("missing refund request id")
		}
		id := args[1]
		if !strings.HasPrefix(id, "rr_") {
			return usage("refund request id must start with rr_, got %q", id)
		}
		var message, reason string
		fs := flag.NewFlagSet("decline", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.StringVar(&message, "message", "", "")
		fs.StringVar(&reason, "reason", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return usage("%v", err)
		}
		if fs.NArg() > 0 {
			return usage("unexpected %q", fs.Arg(0))
		}
		if strings.TrimSpace(message) == "" || strings.TrimSpace(reason) == "" {
			return usage("decline needs --message (emailed to the customer) and --reason (for the audit log)")
		}
		sent, err := c.Payments.DeclineRefundRequest(ctx, id, message, reason, c.Actor)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "refund request %s declined\n", id)
		if !sent {
			return fmt.Errorf("%w: the decline is queued in payment_emails; tell the customer by hand if it does not go", ErrNotNotified)
		}
		return nil
	}
	return usage("unknown refund-request verb %q", args[0])
}
