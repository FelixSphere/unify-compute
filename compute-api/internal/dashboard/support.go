package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/support"
)

// supportWritesPerUser bounds what one user can put in front of the
// operator: every message is read by a person.
var supportWritesPerUser = accounts.Rule{Bucket: "support_write_user", Limit: 20, Window: time.Hour}

type supportMessageView struct {
	ID        int64  `json:"id"`
	Object    string `json:"object"`
	FromStaff bool   `json:"from_staff"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// supportView is a conversation as its user sees it (ADR-0014). Staff are
// "Unify Compute": who answered is in audit_events, not here.
type supportView struct {
	ID        string               `json:"id"`
	Object    string               `json:"object"`
	State     string               `json:"state"`
	Preview   string               `json:"preview"`
	CreatedAt int64                `json:"created_at"`
	UpdatedAt int64                `json:"updated_at"`
	ClosedAt  *int64               `json:"closed_at"`
	Messages  []supportMessageView `json:"messages,omitempty"`
}

func viewSupportMessage(m support.Message) supportMessageView {
	return supportMessageView{ID: m.ID, Object: "compute.support_message", FromStaff: m.FromStaff, Body: m.Body, CreatedAt: m.CreatedAt.Unix()}
}

func viewSupport(c support.Conversation) supportView {
	v := supportView{ID: c.ID, Object: "compute.support_conversation", State: c.State, Preview: c.Preview,
		CreatedAt: c.CreatedAt.Unix(), UpdatedAt: c.UpdatedAt.Unix(), ClosedAt: unixPtr(c.ClosedAt)}
	for _, m := range c.Messages {
		v.Messages = append(v.Messages, viewSupportMessage(m))
	}
	return v
}

type supportBody struct {
	Message string `json:"message"`
}

func (h *Handler) supportUnavailable(w http.ResponseWriter) bool {
	if h.support == nil {
		notImplemented(w, "Support")
		return true
	}
	return false
}

func writeSupportError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, support.ErrNotFound):
		notFound(w)
	case errors.Is(err, support.ErrEmpty):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_message", "Write a message.", "message")
	case errors.Is(err, support.ErrTooBig):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid_message",
			"A message is at most "+strconv.Itoa(support.MaxBody)+" characters.", "message")
	case errors.Is(err, support.ErrLive):
		writeError(w, http.StatusConflict, "conflict", "conversation_open", "You already have an open conversation. Write there.", "")
	case errors.Is(err, support.ErrClosed):
		writeError(w, http.StatusConflict, "conflict", "conversation_closed", "This conversation is closed. Start a new one.", "")
	default:
		return false
	}
	return true
}

// tellOperator emails the operator address after the response, when a
// conversation starts waiting for a person.
func (h *Handler) tellOperator(c support.Conversation) {
	h.sendLater("support operator notice", func(ctx context.Context) error { return h.support.TellOperator(ctx, c) })
}

func (h *Handler) listSupport(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.supportUnavailable(w) {
		return
	}
	cs, err := h.support.ByUser(r.Context(), u.ID)
	if err != nil {
		h.internal(w, "list support conversations", err)
		return
	}
	data := make([]supportView, len(cs))
	for i, c := range cs {
		data[i] = viewSupport(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "has_more": false})
}

// startSupport opens the user's conversation. Any signed-in user may write,
// whatever the account's status: a suspended account is the one that most
// needs to reach a person.
func (h *Handler) startSupport(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.supportUnavailable(w) {
		return
	}
	var req supportBody
	if !decode(w, r, &req) {
		return
	}
	if !h.hit(w, r, supportWritesPerUser, strconv.FormatInt(u.ID, 10)) {
		return
	}
	c, err := h.support.Start(r.Context(), u, req.Message)
	if writeSupportError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "start support conversation", err)
		return
	}
	h.tellOperator(c)
	writeJSON(w, http.StatusCreated, viewSupport(c))
}

func (h *Handler) getSupport(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.supportUnavailable(w) {
		return
	}
	c, err := h.support.Get(r.Context(), u.ID, r.PathValue("id"), 0)
	if writeSupportError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "get support conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, viewSupport(c))
}

func (h *Handler) writeSupport(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.supportUnavailable(w) {
		return
	}
	var req supportBody
	if !decode(w, r, &req) {
		return
	}
	if !h.hit(w, r, supportWritesPerUser, strconv.FormatInt(u.ID, 10)) {
		return
	}
	id := r.PathValue("id")
	m, waiting, err := h.support.Write(r.Context(), u, id, req.Message)
	if writeSupportError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "write support message", err)
		return
	}
	if waiting {
		h.tellOperator(support.Conversation{ID: id, AccountID: u.AccountID, UpdatedAt: m.CreatedAt})
	}
	writeJSON(w, http.StatusCreated, viewSupportMessage(m))
}

func (h *Handler) closeSupport(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if h.supportUnavailable(w) {
		return
	}
	c, err := h.support.Close(r.Context(), u.ID, r.PathValue("id"))
	if writeSupportError(w, err) {
		return
	}
	if err != nil {
		h.internal(w, "close support conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, viewSupport(c))
}
