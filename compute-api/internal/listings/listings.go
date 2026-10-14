// Package listings is the template marketplace's account side (ADR-0012 §2,
// docs/spec/marketplace.md M2): an account submits one templates.json entry
// as a listing, it waits in a queue, the super admin approves or rejects it
// with a reason, and only an approved, listed entry reaches buyers, who see
// "Unify Compute" as its publisher. The shape is Unify Data's brokered
// market (products.review_state, reviewed_by, listed) on compute's tables.
package listings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
)

// Review states.
const (
	Pending  = "pending"
	Approved = "approved"
	Rejected = "rejected"
)

var (
	ErrNotFound   = errors.New("listings: no such listing")
	ErrExists     = errors.New("listings: a listing with this template id exists")
	ErrPlatformID = errors.New("listings: template id belongs to a platform template")
	ErrNotPending = errors.New("listings: listing is not waiting for review")
	ErrNotListed  = errors.New("listings: listing is not listed")
	// ErrClosed is the production gate (ADR-0012 §4): account listings are
	// not approved for, or served to, production until worker egress is
	// controlled or images are built from reviewed source.
	ErrClosed = errors.New("listings: account listings are closed in this environment")
)

// Invalid is a listing whose template would not boot as a curated one. The
// code and field follow internal/template's own errors.
type Invalid struct {
	Code, Field, Msg string
}

func (e *Invalid) Error() string { return e.Msg }

type Listing struct {
	ID           string
	AccountID    int64
	TemplateID   string
	Template     template.Template
	ReviewState  string
	ReviewReason string
	ReviewedBy   string
	ReviewedAt   *time.Time
	Listed       bool
	// Runs is how many jobs were submitted with this template id. The
	// publisher sees the count, never who ran them.
	Runs      int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Public reports whether buyers see the listing.
func (l Listing) Public() bool { return l.Listed && l.ReviewState == Approved }

// Store holds the catalogue a listing's SKU must come from, the runtime
// ceiling, and the platform template ids a listing may not shadow.
type Store struct {
	db       *pgxpool.Pool
	cat      *sku.Catalogue
	ceiling  int64
	platform map[string]bool
	now      func() time.Time
	// open is false in production until ADR-0012 §4's condition holds.
	open bool
}

// Audit writes the audit row for a review decision inside the decision's
// transaction. Wired to accounts.Service.AuditTx by main.
type Audit func(ctx context.Context, tx pgx.Tx, accountID int64, action, reason string, detail map[string]any) error

func New(db *pgxpool.Pool, cat *sku.Catalogue, ceiling int64, platform *template.Set, environment string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	ids := map[string]bool{}
	for _, t := range platform.List() {
		ids[t.ID] = true
	}
	return &Store{db: db, cat: cat, ceiling: ceiling, platform: ids, now: now, open: !strings.EqualFold(environment, "production")}
}

// Open reports whether account listings may be approved and served here.
func (s *Store) Open() bool { return s.open }

// Validate parses one templates.json entry the way the platform file is
// parsed, so the same rules apply, and refuses a platform template's id.
func (s *Store) Validate(raw json.RawMessage) (template.Template, error) {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return template.Template{}, &Invalid{Code: "invalid_listing", Field: "template", Msg: "template must be a JSON object"}
	}
	if s.platform[probe.ID] {
		return template.Template{}, &Invalid{Code: "listing_id_taken", Field: "template.id", Msg: fmt.Sprintf("%q is a platform template", probe.ID)}
	}
	set, err := template.Parse([]byte(`{"templates":[`+string(raw)+`]}`), s.cat, s.ceiling)
	if err != nil {
		return template.Template{}, &Invalid{Code: "invalid_listing", Field: "template", Msg: strings.TrimPrefix(err.Error(), "templates: ")}
	}
	t := set.Templates[0]
	t.Publisher = template.PublisherOfRecord
	return t, nil
}

const columns = `l.id, l.account_id, l.template_id, l.template, l.review_state, COALESCE(l.review_reason, ''),
	COALESCE(l.reviewed_by, ''), l.reviewed_at, l.listed, l.created_at, l.updated_at,
	(SELECT count(*) FROM jobs j WHERE j.template_id = l.template_id)`

func (s *Store) scan(row pgx.Row) (Listing, error) {
	var l Listing
	var raw []byte
	err := row.Scan(&l.ID, &l.AccountID, &l.TemplateID, &raw, &l.ReviewState, &l.ReviewReason,
		&l.ReviewedBy, &l.ReviewedAt, &l.Listed, &l.CreatedAt, &l.UpdatedAt, &l.Runs)
	if errors.Is(err, pgx.ErrNoRows) {
		return Listing{}, ErrNotFound
	}
	if err != nil {
		return Listing{}, err
	}
	if err := json.Unmarshal(raw, &l.Template); err != nil {
		return Listing{}, fmt.Errorf("listings: %s template: %w", l.ID, err)
	}
	l.Template.Object = "compute.template"
	l.Template.Publisher = template.PublisherOfRecord
	// Availability is decided when served, not when stored: the SKU's sale
	// status can change under a stored listing.
	if item, err := s.cat.Get(l.Template.SKU); err == nil && !item.Available && l.Template.Available {
		l.Template.Available = false
		l.Template.UnavailableReason = fmt.Sprintf("default sku %q is not currently sold", l.Template.SKU)
	}
	return l, nil
}

// Create stores a submitted listing as pending.
func (s *Store) Create(ctx context.Context, accountID int64, raw json.RawMessage) (Listing, error) {
	t, err := s.Validate(raw)
	if err != nil {
		return Listing{}, err
	}
	body, _ := json.Marshal(stored(t))
	l, err := s.scan(s.db.QueryRow(ctx, `
		INSERT INTO listings AS l (id, account_id, template_id, template, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING `+columns, store.NewID("lst_", s.now()), accountID, t.ID, body, s.now()))
	if isUnique(err) {
		return Listing{}, ErrExists
	}
	return l, err
}

// stored strips what Parse derives, so the row holds what the publisher
// gave and nothing the catalogue decided at the time.
func stored(t template.Template) template.Template {
	t.Object, t.Publisher = "", ""
	return t
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// ByAccount lists an account's listings, oldest first.
func (s *Store) ByAccount(ctx context.Context, accountID int64) ([]Listing, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM listings l WHERE l.account_id = $1 ORDER BY l.created_at, l.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Listing{}
	for rows.Next() {
		l, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get returns one listing by id, scoped to an account when accountID > 0.
func (s *Store) Get(ctx context.Context, accountID int64, id string) (Listing, error) {
	return s.scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM listings l WHERE l.id = $1 AND ($2 = 0 OR l.account_id = $2)`, id, accountID))
}

// trustFields is what a buyer relies on. Changing any of them returns an
// approved listing to pending (ADR-0012 §2); max_runtime_seconds and the
// image's licence note do not.
func trustFields(t template.Template) string {
	b, _ := json.Marshal(map[string]any{
		"title": t.Title, "description": t.Description, "image": t.Image, "command": t.Command, "env": t.Env,
		"sku": t.SKU, "inputs": t.Inputs, "outputs": t.Outputs, "overridable": t.Overridable,
	})
	return string(b)
}

// Update replaces the listing's template. The listing is relisted and
// returns to pending: approving one version is not approving another, and a
// relisting after an unlisting is a new request, as in Unify Data.
func (s *Store) Update(ctx context.Context, accountID int64, id string, raw json.RawMessage) (Listing, error) {
	old, err := s.Get(ctx, accountID, id)
	if err != nil {
		return Listing{}, err
	}
	t, err := s.Validate(raw)
	if err != nil {
		return Listing{}, err
	}
	if t.ID != old.TemplateID {
		// The slug is the buyer's handle; a new slug is a new listing.
		return Listing{}, &Invalid{Code: "listing_id_fixed", Field: "template.id", Msg: "a listing keeps its template id; submit a new listing for a new id"}
	}
	body, _ := json.Marshal(stored(t))
	return s.scan(s.db.QueryRow(ctx, `
		UPDATE listings AS l SET template = $3, review_state = 'pending', review_reason = NULL, reviewed_by = NULL,
			reviewed_at = NULL, listed = true, updated_at = $4
		WHERE l.id = $1 AND l.account_id = $2
		RETURNING `+columns, id, accountID, body, s.now()))
}

// Unlist takes a listing off the market. Never refused for a listing the
// account owns; already unlisted is a no-op.
func (s *Store) Unlist(ctx context.Context, accountID int64, id string) (Listing, error) {
	return s.scan(s.db.QueryRow(ctx, `
		UPDATE listings AS l SET listed = false, updated_at = $3 WHERE l.id = $1 AND l.account_id = $2
		RETURNING `+columns, id, accountID, s.now()))
}

// Queue is what waits for review, oldest first.
func (s *Store) Queue(ctx context.Context) ([]Listing, error) {
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM listings l WHERE l.review_state = 'pending' AND l.listed ORDER BY l.created_at, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Listing{}
	for rows.Next() {
		l, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Review approves or rejects a pending, listed listing, writing the audit
// row in the same transaction. Approval is refused where account listings
// are closed (ErrClosed). A rejection unlists, as Unify Data's does: the
// publisher sees why, changes it and asks again.
func (s *Store) Review(ctx context.Context, id string, approve bool, reviewer, reason string, audit Audit) (Listing, error) {
	if approve && !s.open {
		return Listing{}, ErrClosed
	}
	if strings.TrimSpace(reason) == "" {
		return Listing{}, errors.New("listings: a review needs a reason")
	}
	var out Listing
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		l, err := s.scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM listings l WHERE l.id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if l.ReviewState != Pending || !l.Listed {
			return ErrNotPending
		}
		state, listed, action := Approved, true, "listing_approved"
		if !approve {
			state, listed, action = Rejected, false, "listing_rejected"
		}
		out, err = s.scan(tx.QueryRow(ctx, `
			UPDATE listings AS l SET review_state = $2, review_reason = $3, reviewed_by = $4, reviewed_at = $5, listed = $6, updated_at = $5
			WHERE l.id = $1 RETURNING `+columns, id, state, reason, reviewer, s.now(), listed))
		if err != nil {
			return err
		}
		if audit != nil {
			return audit(ctx, tx, l.AccountID, action, reason, map[string]any{"listing_id": l.ID, "template_id": l.TemplateID})
		}
		return nil
	})
	return out, err
}

// AdminUnlist takes any listing off the market, with a reason, audited.
func (s *Store) AdminUnlist(ctx context.Context, id, reviewer, reason string, audit Audit) (Listing, error) {
	if strings.TrimSpace(reason) == "" {
		return Listing{}, errors.New("listings: unlist needs a reason")
	}
	var out Listing
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		l, err := s.scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM listings l WHERE l.id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if !l.Listed {
			return ErrNotListed
		}
		out, err = s.scan(tx.QueryRow(ctx, `UPDATE listings AS l SET listed = false, updated_at = $2 WHERE l.id = $1 RETURNING `+columns, id, s.now()))
		if err != nil {
			return err
		}
		if audit != nil {
			return audit(ctx, tx, l.AccountID, "listing_unlisted", reason, map[string]any{"listing_id": l.ID, "template_id": l.TemplateID, "by": reviewer})
		}
		return nil
	})
	return out, err
}

// Approved is what buyers see beside the platform templates: approved and
// listed, as templates. Empty where account listings are closed.
func (s *Store) Approved(ctx context.Context) ([]template.Template, error) {
	if !s.open {
		return []template.Template{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+columns+` FROM listings l WHERE l.listed AND l.review_state = 'approved' ORDER BY l.template_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []template.Template{}
	for rows.Next() {
		l, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l.Template)
	}
	return out, rows.Err()
}

// Template resolves a buyer's `template` id to an approved, listed listing's
// template; template.ErrUnknown otherwise, so a submit answers
// unknown_template like a missing platform template.
func (s *Store) Template(ctx context.Context, templateID string) (template.Template, error) {
	if !s.open {
		return template.Template{}, template.ErrUnknown
	}
	l, err := s.scan(s.db.QueryRow(ctx, `SELECT `+columns+` FROM listings l WHERE l.template_id = $1 AND l.listed AND l.review_state = 'approved'`, templateID))
	if errors.Is(err, ErrNotFound) {
		return template.Template{}, template.ErrUnknown
	}
	if err != nil {
		return template.Template{}, err
	}
	return l.Template, nil
}
