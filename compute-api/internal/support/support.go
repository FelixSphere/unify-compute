// Package support is the customer support conversation (ADR-0014,
// docs/spec/accounts.md "Support conversations"): a signed-in user writes
// from the dashboard, the operator answers with `compute-api admin support`,
// and the user is emailed that there is an answer. There is no AI first
// responder and no web inbox; the shape is Unify Data's feedback thread
// (messages with from_staff, a state that says whose turn it is) on
// compute's tables.
//
// A conversation belongs to one user. A user has at most one that is not
// closed, so writing again continues it; a closed one is history, and the
// next message starts a new conversation.
package support

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// States. Open waits for a person; Answered waits for the customer.
const (
	Open     = "open"
	Answered = "answered"
	Closed   = "closed"
)

// MaxBody is the longest message, in characters, for either side.
const MaxBody = 4000

var (
	ErrNotFound = errors.New("support: no such conversation")
	// ErrLive: the user already has a conversation that is not closed.
	ErrLive   = errors.New("support: a conversation is already open")
	ErrClosed = errors.New("support: conversation is closed")
	ErrEmpty  = errors.New("support: message is empty")
	ErrTooBig = errors.New("support: message is too long")
)

type Message struct {
	ID        int64
	FromStaff bool
	Body      string
	CreatedAt time.Time
}

type Conversation struct {
	ID        string
	AccountID int64
	UserID    int64
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	// LastMessageID and Preview describe the newest message; Messages is
	// filled by Get only.
	LastMessageID int64
	Preview       string
	Messages      []Message
}

// Mailer is what the service sends (internal/mailer).
type Mailer interface {
	SupportReply(ctx context.Context, to string, r mailer.SupportAnswer) error
	Operator(ctx context.Context, to string, n mailer.OperatorNotice) error
}

// Audit writes a staff action's audit row inside its transaction; wired to
// the admin CLI's audit by its caller.
type Audit func(ctx context.Context, tx pgx.Tx, accountID int64, action string, detail map[string]any) error

type Service struct {
	db *pgxpool.Pool
	// mail is nil when the deployment sends no account mail; then nothing
	// is mailed and Reply says so.
	mail     Mailer
	operator string
	now      func() time.Time
}

// New: operatorEmail is OPERATOR_EMAIL, where a waiting conversation is
// announced; empty is the mailer's default operator address.
func New(db *pgxpool.Pool, mail Mailer, operatorEmail string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if strings.TrimSpace(operatorEmail) == "" {
		operatorEmail = mailer.DefaultOperator
	}
	return &Service{db: db, mail: mail, operator: strings.TrimSpace(operatorEmail), now: now}
}

func clean(body string) (string, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" || !utf8.ValidString(body) {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(body) > MaxBody {
		return "", ErrTooBig
	}
	return body, nil
}

const columns = `c.id, c.account_id, c.user_id, c.state, c.created_at, c.updated_at, c.closed_at,
	COALESCE((SELECT max(m.id) FROM support_messages m WHERE m.conversation_id = c.id), 0),
	COALESCE((SELECT left(m.body, 140) FROM support_messages m WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1), '')`

func scan(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.AccountID, &c.UserID, &c.State, &c.CreatedAt, &c.UpdatedAt, &c.ClosedAt, &c.LastMessageID, &c.Preview)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return c, err
}

func list(ctx context.Context, db *pgxpool.Pool, where string, args ...any) ([]Conversation, error) {
	rows, err := db.Query(ctx, `SELECT `+columns+` FROM support_conversations c `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func insert(ctx context.Context, tx pgx.Tx, id string, staff bool, author, body string, at time.Time) (Message, error) {
	m := Message{FromStaff: staff, Body: body, CreatedAt: at}
	err := tx.QueryRow(ctx, `INSERT INTO support_messages (conversation_id, from_staff, author, body, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, id, staff, author, body, at).Scan(&m.ID)
	return m, err
}

// Start opens the user's conversation with its first message.
func (s *Service) Start(ctx context.Context, u accounts.User, body string) (Conversation, error) {
	body, err := clean(body)
	if err != nil {
		return Conversation{}, err
	}
	now := s.now()
	id := store.NewID("sc_", now)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO support_conversations (id, account_id, user_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)`, id, u.AccountID, u.ID, now); err != nil {
			return err
		}
		_, err := insert(ctx, tx, id, false, strconv.FormatInt(u.ID, 10), body, now)
		return err
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return Conversation{}, ErrLive
	}
	if err != nil {
		return Conversation{}, err
	}
	return s.Get(ctx, u.ID, id, 0)
}

// ByUser lists the user's conversations, newest first.
func (s *Service) ByUser(ctx context.Context, userID int64) ([]Conversation, error) {
	return list(ctx, s.db, `WHERE c.user_id = $1 ORDER BY c.created_at DESC, c.id DESC`, userID)
}

// Waiting lists every conversation waiting for a person, longest wait first.
func (s *Service) Waiting(ctx context.Context) ([]Conversation, error) {
	return list(ctx, s.db, `WHERE c.state = 'open' ORDER BY c.updated_at, c.id`)
}

// Get returns one conversation with its messages after message id `after`,
// scoped to a user when userID > 0 (0 is the operator).
func (s *Service) Get(ctx context.Context, userID int64, id string, after int64) (Conversation, error) {
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM support_conversations c
		WHERE c.id = $1 AND ($2 = 0 OR c.user_id = $2)`, id, userID))
	if err != nil {
		return Conversation{}, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, from_staff, body, created_at FROM support_messages
		WHERE conversation_id = $1 AND id > $2 ORDER BY id`, id, after)
	if err != nil {
		return Conversation{}, err
	}
	defer rows.Close()
	c.Messages = []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.FromStaff, &m.Body, &m.CreatedAt); err != nil {
			return Conversation{}, err
		}
		c.Messages = append(c.Messages, m)
	}
	return c, rows.Err()
}

// lock reads the conversation's owner and state FOR UPDATE.
func lock(ctx context.Context, tx pgx.Tx, userID int64, id string) (accountID, owner int64, state string, err error) {
	err = tx.QueryRow(ctx, `SELECT account_id, user_id, state FROM support_conversations
		WHERE id = $1 AND ($2 = 0 OR user_id = $2) FOR UPDATE`, id, userID).Scan(&accountID, &owner, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// Write adds the user's message. The conversation waits for a person
// again; waiting reports whether it was not waiting before, which is when
// the operator is told.
func (s *Service) Write(ctx context.Context, u accounts.User, id, body string) (m Message, waiting bool, err error) {
	if body, err = clean(body); err != nil {
		return Message{}, false, err
	}
	now := s.now()
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, _, state, err := lock(ctx, tx, u.ID, id)
		if err != nil {
			return err
		}
		if state == Closed {
			return ErrClosed
		}
		waiting = state != Open
		if m, err = insert(ctx, tx, id, false, strconv.FormatInt(u.ID, 10), body, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE support_conversations SET state = 'open', updated_at = $2 WHERE id = $1`, id, now)
		return err
	})
	return m, waiting, err
}

// Close ends the user's own conversation; closing a closed one is not an error.
func (s *Service) Close(ctx context.Context, userID int64, id string) (Conversation, error) {
	if err := s.close(ctx, userID, id, "user", nil); err != nil {
		return Conversation{}, err
	}
	return s.Get(ctx, userID, id, 0)
}

// AdminClose ends a conversation from the admin CLI.
func (s *Service) AdminClose(ctx context.Context, id, actor string, audit Audit) error {
	return s.close(ctx, 0, id, actor, audit)
}

func (s *Service) close(ctx context.Context, userID int64, id, by string, audit Audit) error {
	now := s.now()
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		accountID, _, state, err := lock(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		if state == Closed {
			if audit != nil {
				return ErrClosed
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE support_conversations SET state = 'closed', closed_at = $2, closed_by = $3, updated_at = $2
			WHERE id = $1`, id, now, by); err != nil {
			return err
		}
		if audit != nil {
			return audit(ctx, tx, accountID, accounts.ActionSupportClosed, map[string]any{"conversation_id": id})
		}
		return nil
	})
}

// Reply adds a staff message and marks the conversation answered, with its
// audit row, in one transaction; then emails the user that there is an
// answer. mailed is false when that email did not go: the reply is saved
// either way, and the customer sees it on the Support page.
func (s *Service) Reply(ctx context.Context, id, actor, body string, audit Audit) (m Message, mailed bool, err error) {
	if body, err = clean(body); err != nil {
		return Message{}, false, err
	}
	now := s.now()
	var owner int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		accountID, user, state, err := lock(ctx, tx, 0, id)
		if err != nil {
			return err
		}
		if state == Closed {
			return ErrClosed
		}
		owner = user
		if m, err = insert(ctx, tx, id, true, actor, body, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE support_conversations SET state = 'answered', updated_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		return audit(ctx, tx, accountID, accounts.ActionSupportReplied, map[string]any{"conversation_id": id, "message_id": m.ID})
	})
	if err != nil || s.mail == nil {
		return m, false, err
	}
	to, err := s.Email(ctx, owner)
	if err != nil || to == "" {
		return m, false, nil
	}
	return m, s.mail.SupportReply(ctx, to, mailer.SupportAnswer{ConversationID: id, At: now}) == nil, nil
}

// Email is the address of an active user, "" when the user is gone or
// cannot log in (and so could not read the answer).
func (s *Service) Email(ctx context.Context, userID int64) (string, error) {
	var email string
	err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id = $1 AND status = $2`, userID, accounts.UserActive).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// TellOperator emails the operator address that a conversation waits for a
// person. It carries no customer text: that stays in the database.
func (s *Service) TellOperator(ctx context.Context, c Conversation) error {
	if s.mail == nil {
		return nil
	}
	return s.mail.Operator(ctx, s.operator, mailer.OperatorNotice{
		Title:  "A support conversation is waiting",
		Body:   "A customer wrote to Unify Compute support and is waiting for a person.",
		Action: "Read it with `compute-api admin support show " + c.ID + "` and answer with `compute-api admin support reply " + c.ID + " --message …`.",
		Facts: [][2]string{{"Conversation", c.ID}, {"Account", strconv.FormatInt(c.AccountID, 10)},
			{"Waiting since", c.UpdatedAt.UTC().Format("2006-01-02 15:04 UTC")}},
	})
}
