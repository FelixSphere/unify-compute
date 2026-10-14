package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
)

// listing is `compute-api admin listing …` (spec/marketplace.md M2): the
// super admin's side of the brokered market. Every decision carries a
// reason and lands in audit_events with the change, like Unify Data's
// staff_actions.
//
//	listing queue
//	listing show <id>
//	listing approve <id> --reason TEXT
//	listing reject <id> --reason TEXT
//	listing unlist <id> --reason TEXT
func (c *CLI) listing(ctx context.Context, args []string) error {
	if c.Listings == nil {
		return usage("this deployment has no account listings (ACCOUNTS_BACKEND is not local)")
	}
	if len(args) == 0 {
		return usage("listing needs a verb")
	}
	switch args[0] {
	case "queue":
		if len(args) > 1 {
			return usage("unexpected %q", args[1])
		}
		return c.listingQueue(ctx)
	case "show", "approve", "reject", "unlist":
	default:
		return usage("unknown listing verb %q", args[0])
	}
	v, err := parse(args, false)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(v.target, "lst_") {
		return usage("listing id must start with lst_, got %q", v.target)
	}
	switch v.name {
	case "show":
		l, err := c.Listings.Get(ctx, 0, v.target)
		if err != nil {
			return describe(err)
		}
		return c.printListing(l, true)
	case "approve", "reject":
		reason, err := v.requireReason()
		if err != nil {
			return err
		}
		l, err := c.Listings.Review(ctx, v.target, v.name == "approve", c.Actor, reason, c.listingAudit)
		if err != nil {
			return describe(err)
		}
		fmt.Fprintf(c.Out, "%s %s (%s) by %s: %s\n", l.ReviewState, l.ID, l.TemplateID, c.Actor, reason)
		return nil
	case "unlist":
		reason, err := v.requireReason()
		if err != nil {
			return err
		}
		l, err := c.Listings.AdminUnlist(ctx, v.target, c.Actor, reason, c.listingAudit)
		if err != nil {
			return describe(err)
		}
		fmt.Fprintf(c.Out, "unlisted %s (%s): %s\n", l.ID, l.TemplateID, reason)
		return nil
	}
	return nil
}

func describe(err error) error {
	switch {
	case errors.Is(err, listings.ErrNotFound):
		return usage("no such listing")
	case errors.Is(err, listings.ErrNotPending):
		return errors.New("admin: that listing is not waiting for review")
	case errors.Is(err, listings.ErrNotListed):
		return errors.New("admin: that listing is already unlisted")
	case errors.Is(err, listings.ErrClosed):
		return errors.New("admin: account listings are closed in this environment (ADR-0012 §4): approve on staging only until worker egress is controlled or images are built from reviewed source")
	}
	return err
}

func (c *CLI) listingAudit(ctx context.Context, tx pgx.Tx, accountID int64, action, reason string, detail map[string]any) error {
	_, err := c.auditEvent(ctx, tx, accountID, action, reason, detail)
	return err
}

func (c *CLI) listingQueue(ctx context.Context) error {
	q, err := c.Listings.Queue(ctx)
	if err != nil {
		return err
	}
	if len(q) == 0 {
		fmt.Fprintln(c.Out, "queue is empty")
		return nil
	}
	fmt.Fprintf(c.Out, "%d waiting for review (oldest first)\n", len(q))
	for _, l := range q {
		if err := c.printListing(l, false); err != nil {
			return err
		}
	}
	return nil
}

// printListing shows the super admin both sides: the account, which buyers
// never see, and the template as buyers would.
func (c *CLI) printListing(l listings.Listing, full bool) error {
	fmt.Fprintf(c.Out, "%s  account %d  template %s  %s  listed=%t  runs=%d  submitted %s\n",
		l.ID, l.AccountID, l.TemplateID, l.ReviewState, l.Listed, l.Runs, l.CreatedAt.UTC().Format("2006-01-02 15:04"))
	fmt.Fprintf(c.Out, "    %s — %s\n", l.Template.Title, l.Template.Description)
	fmt.Fprintf(c.Out, "    image %s\n    sku %s  runtime %ds  inputs %d  outputs %d\n", l.Template.Image, l.Template.SKU,
		l.Template.MaxRuntimeSeconds, len(l.Template.Inputs), len(l.Template.Outputs))
	if l.Template.ImageSource != "" {
		fmt.Fprintf(c.Out, "    image source: %s\n", l.Template.ImageSource)
	}
	if l.ReviewReason != "" {
		fmt.Fprintf(c.Out, "    review: %s by %s\n", l.ReviewReason, l.ReviewedBy)
	}
	if full {
		b, err := json.MarshalIndent(l.Template, "    ", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "    %s\n", b)
	}
	return nil
}
