package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidEventCursor = errors.New("store: events cursor does not name an event of this job")

type Event struct {
	ID        string
	Seq       int64
	JobID     string
	Type      string
	Data      json.RawMessage
	CreatedAt time.Time
}

// NewEvent is what an emitter appends. The store checks only what the schema
// can; which types exist and what their data must carry is events.Validate.
type NewEvent struct {
	JobID string
	Type  string
	Data  json.RawMessage
}

const eventColumns = `id, seq, job_id, type, data, created_at`

func scanEvent(row pgx.Row) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.Seq, &e.JobID, &e.Type, &e.Data, &e.CreatedAt)
	return e, err
}

// AppendEvent writes the event and queues a delivery for every webhook that
// should receive it, in one statement: an event that exists always has its
// deliveries, which is what makes webhook delivery at-least-once rather than
// at-most-once. A job id that does not exist is ErrNotFound.
func (s *Store) AppendEvent(ctx context.Context, ne NewEvent) (Event, error) {
	data := ne.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	ev, err := scanEvent(s.pool.QueryRow(ctx, `
		WITH ev AS (
			INSERT INTO job_events (id, job_id, type, data)
			SELECT $1, id, $3, $4 FROM jobs WHERE id = $2
			RETURNING `+eventColumns+`
		), fan AS (
			INSERT INTO webhook_deliveries (event_id, webhook_id, next_attempt_at)
			SELECT ev.id, w.id, ev.created_at
			FROM ev
			JOIN jobs j ON j.id = ev.job_id
			JOIN webhooks w ON w.owner = j.owner
				AND (w.token_id IS NULL OR w.token_id = j.token_id)
				AND ev.type = ANY (w.event_types)
		)
		SELECT `+eventColumns+` FROM ev`,
		NewEventID(time.Now()), ne.JobID, ne.Type, []byte(data)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("store: append event: %w", err)
	}
	return ev, nil
}

const (
	DefaultEventLimit = 50
	MaxEventLimit     = 100
)

type EventPage struct {
	Events  []Event
	HasMore bool
}

// ListEvents returns a job's events oldest first, after the event named by
// after (empty for the start). It is owner-scoped like Get: another owner's
// job and a missing one are both ErrNotFound.
func (s *Store) ListEvents(ctx context.Context, owner, jobID, after string, limit int) (EventPage, error) {
	if limit <= 0 {
		limit = DefaultEventLimit
	}
	if limit > MaxEventLimit {
		limit = MaxEventLimit
	}
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM jobs WHERE owner = $1 AND id = $2`, owner, jobID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventPage{}, ErrNotFound
	}
	if err != nil {
		return EventPage{}, err
	}
	var cursor int64
	if after != "" {
		err := s.pool.QueryRow(ctx, `SELECT seq FROM job_events WHERE job_id = $1 AND id = $2`, jobID, after).Scan(&cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			return EventPage{}, ErrInvalidEventCursor
		}
		if err != nil {
			return EventPage{}, err
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+eventColumns+` FROM job_events
		WHERE job_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3`, jobID, cursor, limit+1)
	if err != nil {
		return EventPage{}, err
	}
	evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) { return scanEvent(r) })
	if err != nil {
		return EventPage{}, err
	}
	page := EventPage{Events: evs}
	if len(evs) > limit {
		page.Events = evs[:limit]
		page.HasMore = true
	}
	return page, nil
}

type Webhook struct {
	ID         string
	Owner      string
	TokenID    *int64
	URL        string
	Secret     string
	EventTypes []string
	CreatedAt  time.Time
}

// NewWebhook is a registration. TokenID nil makes it owner-wide; the public
// API always sets it (docs/spec/events.md).
type NewWebhook struct {
	Owner      string
	TokenID    *int64
	URL        string
	Secret     string
	EventTypes []string
}

func (s *Store) CreateWebhook(ctx context.Context, nw NewWebhook) (Webhook, error) {
	w := Webhook{Owner: nw.Owner, TokenID: nw.TokenID, URL: nw.URL, Secret: nw.Secret, EventTypes: nw.EventTypes}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO webhooks (id, owner, token_id, url, secret, event_types)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		NewWebhookID(time.Now()), nw.Owner, nw.TokenID, nw.URL, nw.Secret, nw.EventTypes).Scan(&w.ID, &w.CreatedAt)
	if err != nil {
		return Webhook{}, fmt.Errorf("store: create webhook: %w", err)
	}
	return w, nil
}

// Delivery is one claimed attempt: the event to send, where, and with what
// secret. Attempt counts this one.
type Delivery struct {
	ID        int64
	Attempt   int
	Event     Event
	WebhookID string
	URL       string
	Secret    string
}

// ClaimDeliveries takes up to limit pending deliveries due at now, counts the
// attempt, and pushes their next_attempt_at out by lease so no other worker
// picks them up meanwhile. A worker that dies mid-attempt loses only the
// lease: the delivery comes due again and is retried, so a crash costs a
// duplicate, never a lost delivery.
func (s *Store) ClaimDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM webhook_deliveries
			WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE webhook_deliveries d
			SET attempts = d.attempts + 1, next_attempt_at = $1::timestamptz + make_interval(secs => $2)
			FROM due WHERE d.id = due.id
			RETURNING d.id, d.attempts, d.event_id, d.webhook_id
		)
		SELECT c.id, c.attempts, c.webhook_id, w.url, w.secret,
			e.id, e.seq, e.job_id, e.type, e.data, e.created_at
		FROM claimed c
		JOIN job_events e ON e.id = c.event_id
		JOIN webhooks w ON w.id = c.webhook_id
		ORDER BY c.id`,
		now, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Delivery, error) {
		var d Delivery
		e := &d.Event
		err := r.Scan(&d.ID, &d.Attempt, &d.WebhookID, &d.URL, &d.Secret,
			&e.ID, &e.Seq, &e.JobID, &e.Type, &e.Data, &e.CreatedAt)
		return d, err
	})
}

// MarkDelivered and MarkAttemptFailed record the outcome of the attempt a
// claim returned. Both match on the attempt number, so a worker whose lease
// expired and was overtaken cannot overwrite the newer attempt's result.
func (s *Store) MarkDelivered(ctx context.Context, id int64, attempt int, now time.Time, status int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = 'delivered', delivered_at = $3, last_status = $4, last_error = NULL
		WHERE id = $1 AND attempts = $2 AND status = 'pending'`,
		id, attempt, now, status)
	return err
}

// MarkAttemptFailed schedules the next attempt at retryAt, or gives up when
// retryAt is nil. status is the receiver's HTTP status, nil when there was
// no response.
func (s *Store) MarkAttemptFailed(ctx context.Context, id int64, attempt int, status *int, reason string, retryAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status          = CASE WHEN $5::timestamptz IS NULL THEN 'failed' ELSE 'pending' END,
		    next_attempt_at = COALESCE($5, next_attempt_at),
		    last_status     = $3,
		    last_error      = $4
		WHERE id = $1 AND attempts = $2 AND status = 'pending'`,
		id, attempt, status, reason, retryAt)
	return err
}

type DeliveryState struct {
	WebhookID     string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastStatus    *int
	LastError     *string
	DeliveredAt   *time.Time
}

// DeliveriesOf reports every delivery of one event, for operators and tests.
func (s *Store) DeliveriesOf(ctx context.Context, eventID string) ([]DeliveryState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT webhook_id, status, attempts, next_attempt_at, last_status, last_error, delivered_at
		FROM webhook_deliveries WHERE event_id = $1 ORDER BY webhook_id`, eventID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeliveryState, error) {
		var d DeliveryState
		err := r.Scan(&d.WebhookID, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.LastStatus, &d.LastError, &d.DeliveredAt)
		return d, err
	})
}
