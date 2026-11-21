package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixSphere/unify-compute/compute-api/internal/support"
)

// ErrReplyNotMailed means a support reply was saved, but the email telling
// the customer was not sent. main exits 1 so a script notices.
var ErrReplyNotMailed = errors.New("the reply is saved and shows on the customer's Support page, but the email telling them was not sent")

// maxStdin bounds `support reply --stdin`; a message is at most
// support.MaxBody characters, four bytes each.
const maxStdin = 4*support.MaxBody + 1

// supportReason is the audit reason of a reply or a close: the verb is its
// own reason, and what was said is the message row the audit event names.
const supportReason = "support conversation"

type supportMessageJSON struct {
	ID        int64  `json:"id"`
	FromStaff bool   `json:"from_staff"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// supportJSON is what `--json` prints, for the operator's tooling. list
// carries no customer text and no address; show carries both.
type supportJSON struct {
	ID            string               `json:"id"`
	AccountID     int64                `json:"account_id"`
	UserID        int64                `json:"user_id"`
	State         string               `json:"state"`
	LastMessageID int64                `json:"last_message_id"`
	CreatedAt     int64                `json:"created_at"`
	UpdatedAt     int64                `json:"updated_at"`
	Email         string               `json:"email,omitempty"`
	Messages      []supportMessageJSON `json:"messages,omitempty"`
}

func supportRow(c support.Conversation) supportJSON {
	return supportJSON{ID: c.ID, AccountID: c.AccountID, UserID: c.UserID, State: c.State, LastMessageID: c.LastMessageID,
		CreatedAt: c.CreatedAt.Unix(), UpdatedAt: c.UpdatedAt.Unix()}
}

// support is `compute-api admin support …` (ADR-0014): the operator's side
// of the customer conversation.
//
//	support list [--json]                          waiting for a person, longest wait first
//	support show <sc_id> [--after N] [--json]      the messages (after message N)
//	support reply <sc_id> --message TEXT | --stdin posts as staff; the customer is emailed
//	support close <sc_id>
//
// reply and close take no --reason: the audit row names the conversation
// and the message, which say why.
func (c *CLI) support(ctx context.Context, args []string) error {
	if c.Support == nil {
		return usage("this deployment has no support conversations (ACCOUNTS_BACKEND is not local)")
	}
	if len(args) == 0 {
		return usage("support needs a verb")
	}
	verb := args[0]
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON, stdin bool
	var message string
	var after int64
	rest := args[1:]
	id := ""
	switch verb {
	case "list":
		fs.BoolVar(&asJSON, "json", false, "")
	case "show":
		fs.BoolVar(&asJSON, "json", false, "")
		fs.Int64Var(&after, "after", 0, "")
	case "reply":
		fs.StringVar(&message, "message", "", "")
		fs.BoolVar(&stdin, "stdin", false, "")
	case "close":
	default:
		return usage("unknown support verb %q", verb)
	}
	if verb != "list" {
		if len(rest) == 0 || !strings.HasPrefix(rest[0], "sc_") {
			return usage("support %s needs a conversation id (sc_…)", verb)
		}
		id, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return usage("%v", err)
	}
	if fs.NArg() > 0 {
		return usage("unexpected %q", fs.Arg(0))
	}
	switch verb {
	case "list":
		return c.supportList(ctx, asJSON)
	case "show":
		return c.supportShow(ctx, id, after, asJSON)
	case "reply":
		if stdin == (message != "") {
			return usage("support reply needs --message TEXT or --stdin, not both")
		}
		if stdin {
			if c.In == nil {
				return usage("no standard input to read the message from")
			}
			raw, err := io.ReadAll(io.LimitReader(c.In, maxStdin))
			if err != nil {
				return err
			}
			if len(raw) == maxStdin {
				return describeSupport(support.ErrTooBig)
			}
			message = string(raw)
		}
		m, mailed, err := c.Support.Reply(ctx, id, c.Actor, message, c.supportAudit)
		if err != nil {
			return describeSupport(err)
		}
		fmt.Fprintf(c.Out, "replied %s message %d\n", id, m.ID)
		if !mailed {
			return ErrReplyNotMailed
		}
		return nil
	case "close":
		if err := c.Support.AdminClose(ctx, id, c.Actor, c.supportAudit); err != nil {
			return describeSupport(err)
		}
		fmt.Fprintf(c.Out, "closed %s\n", id)
	}
	return nil
}

func describeSupport(err error) error {
	switch {
	case errors.Is(err, support.ErrNotFound):
		return usage("no such conversation")
	case errors.Is(err, support.ErrClosed):
		return errors.New("admin: that conversation is closed; the customer's next message starts a new one")
	case errors.Is(err, support.ErrEmpty):
		return usage("the message is empty")
	case errors.Is(err, support.ErrTooBig):
		return usage("the message is longer than %d characters", support.MaxBody)
	}
	return err
}

func (c *CLI) supportAudit(ctx context.Context, tx pgx.Tx, accountID int64, action string, detail map[string]any) error {
	_, err := c.auditEvent(ctx, tx, accountID, action, supportReason, detail)
	return err
}

func (c *CLI) supportList(ctx context.Context, asJSON bool) error {
	list, err := c.Support.Waiting(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		rows := make([]supportJSON, len(list))
		for i, cv := range list {
			rows[i] = supportRow(cv)
		}
		return json.NewEncoder(c.Out).Encode(rows)
	}
	if len(list) == 0 {
		fmt.Fprintln(c.Out, "no conversation is waiting")
		return nil
	}
	fmt.Fprintf(c.Out, "%d waiting for a person (longest wait first)\n", len(list))
	for _, cv := range list {
		fmt.Fprintf(c.Out, "%s  account %d  user %d  waiting since %s  %q\n", cv.ID, cv.AccountID, cv.UserID, stamp(cv.UpdatedAt), cv.Preview)
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04Z") }

func (c *CLI) supportShow(ctx context.Context, id string, after int64, asJSON bool) error {
	cv, err := c.Support.Get(ctx, 0, id, after)
	if err != nil {
		return describeSupport(err)
	}
	email, err := c.Support.Email(ctx, cv.UserID)
	if err != nil {
		return err
	}
	if asJSON {
		row := supportRow(cv)
		row.Email = email
		for _, m := range cv.Messages {
			row.Messages = append(row.Messages, supportMessageJSON{ID: m.ID, FromStaff: m.FromStaff, Body: m.Body, CreatedAt: m.CreatedAt.Unix()})
		}
		return json.NewEncoder(c.Out).Encode(row)
	}
	if email == "" {
		email = "(no active user)"
	}
	fmt.Fprintf(c.Out, "%s  %s  account %d  user %d  %s  opened %s\n", cv.ID, cv.State, cv.AccountID, cv.UserID, email, stamp(cv.CreatedAt))
	for _, m := range cv.Messages {
		who := "customer"
		if m.FromStaff {
			who = "staff"
		}
		fmt.Fprintf(c.Out, "\n[%d] %s  %s\n%s\n", m.ID, who, stamp(m.CreatedAt), m.Body)
	}
	return nil
}
