package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
)

// Members and invitations (spec/accounts.md "Members and roles", task A5b).
// Role checks come before the reauth check, so a member is told "not your
// role" rather than asked for a password first.

func init() {
	for code, s := range map[string]struct {
		status int
		typ    string
	}{
		"role_required":        {http.StatusForbidden, "permission_error"},
		"invalid_role":         {http.StatusBadRequest, "invalid_request"},
		"already_member":       {http.StatusConflict, "conflict"},
		"member_limit_reached": {http.StatusForbidden, "permission_error"},
		"last_owner":           {http.StatusConflict, "conflict"},
		"email_in_use":         {http.StatusConflict, "conflict"},
	} {
		statusOf[code] = s
	}
}

func (h *Handler) registerMembers() {
	h.handle("PATCH /account", h.session(h.renameAccount))
	h.handle("GET /members", h.session(h.read(h.listMembers)))
	h.handle("PATCH /members/{user_id}", h.session(h.changeRole))
	h.handle("DELETE /members/{user_id}", h.session(h.removeMember))
	h.handle("GET /members/invitations", h.session(h.read(h.listInvitations)))
	h.handle("POST /members/invitations", h.session(h.invite))
	h.handle("POST /members/invitations/{id}/resend", h.session(h.resendInvitation))
	h.handle("DELETE /members/invitations/{id}", h.session(h.revokeInvitation))
	h.handle("POST /invitations/lookup", h.lookupInvitation)
	h.handle("POST /invitations/accept", h.acceptInvitation)
}

var errReauthMembers = &accounts.Error{Code: "reauth_required", Message: "Enter your password again to change who is in this account."}

func (h *Handler) fresh(w http.ResponseWriter, s accounts.Session) bool {
	if !s.ReauthFresh(h.acc.Now()) {
		writeAccountsError(w, errReauthMembers)
		return false
	}
	return true
}

func writeErr(h *Handler, w http.ResponseWriter, what string, err error) {
	var ae *accounts.Error
	switch {
	case errors.As(err, &ae):
		writeAccountsError(w, ae)
	case errors.Is(err, accounts.ErrNotFound):
		notFound(w)
	default:
		h.internal(w, what, err)
	}
}

type renameRequest struct {
	Name string `json:"name"`
}

func (h *Handler) renameAccount(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.fresh(w, s) {
		return
	}
	var req renameRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	before, err := h.acc.AccountByID(ctx, u.AccountID)
	if err != nil {
		h.internal(w, "load account", err)
		return
	}
	a, err := h.acc.RenameAccount(ctx, u.AccountID, req.Name)
	if err != nil {
		writeErr(h, w, "rename account", err)
		return
	}
	if a.Name != before.Name {
		h.audit(ctx, accounts.ActionAccountRenamed, clientInfo(r), u.ID, u.AccountID, map[string]any{"from": before.Name, "to": a.Name})
	}
	writeJSON(w, http.StatusOK, map[string]accountView{"account": viewAccount(a)})
}

type memberView struct {
	UserID      int64  `json:"user_id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt *int64 `json:"last_login_at"`
}

func viewMember(u accounts.User) memberView {
	return memberView{UserID: u.ID, Email: u.Email, Role: u.Role, Status: u.Status, CreatedAt: u.CreatedAt.Unix(), LastLoginAt: unixPtr(u.LastLoginAt)}
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	list, err := h.acc.ListMembers(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "list members", err)
		return
	}
	out := make([]memberView, len(list))
	for i, m := range list {
		out[i] = viewMember(m)
	}
	writeJSON(w, http.StatusOK, out)
}

func memberID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("user_id"), 10, 64)
	if err != nil || id < 1 {
		notFound(w)
		return 0, false
	}
	return id, true
}

type roleRequest struct {
	Role string `json:"role"`
}

// changeRole is the owner's alone: it is how ownership changes hands.
func (h *Handler) changeRole(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if u.Role != accounts.RoleOwner {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.fresh(w, s) {
		return
	}
	id, ok := memberID(w, r)
	if !ok {
		return
	}
	var req roleRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	m, old, err := h.acc.ChangeRole(ctx, u.AccountID, id, req.Role)
	if err != nil {
		writeErr(h, w, "change role", err)
		return
	}
	if old != m.Role {
		h.audit(ctx, accounts.ActionRoleChanged, clientInfo(r), u.ID, u.AccountID,
			map[string]any{"user_id": m.ID, "from": old, "to": m.Role})
	}
	writeJSON(w, http.StatusOK, viewMember(m))
}

// removeMember: an owner removes anyone, an admin removes members, anyone
// removes themself. The removed user's sessions end at once, this one's too
// when it removes itself.
func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	id, ok := memberID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	target, err := h.acc.Member(ctx, u.AccountID, id)
	if err != nil {
		writeErr(h, w, "load member", err)
		return
	}
	if !accounts.CanRemove(u, target) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.fresh(w, s) {
		return
	}
	a, err := h.acc.AccountByID(ctx, u.AccountID)
	if err != nil {
		h.internal(w, "load account", err)
		return
	}
	removed, err := h.acc.RemoveMember(ctx, u.AccountID, id)
	if err != nil {
		writeErr(h, w, "remove member", err)
		return
	}
	h.audit(ctx, accounts.ActionMemberRemoved, clientInfo(r), u.ID, u.AccountID,
		map[string]any{"user_id": removed.ID, "role": removed.Role, "self": removed.ID == u.ID})
	by := u.Email
	if removed.ID == u.ID {
		by = ""
	}
	msg := mailer.Removal{AccountName: a.Name, ByEmail: by, At: h.now()}
	h.sendLater("member removed email", func(ctx context.Context) error {
		return h.mail.RemovedFromAccount(ctx, removed.Email, msg)
	})
	if removed.ID == u.ID {
		clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

type invitationView struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	Role           string `json:"role"`
	Status         string `json:"status"`
	InvitedByEmail string `json:"invited_by_email"`
	CreatedAt      int64  `json:"created_at"`
	ExpiresAt      int64  `json:"expires_at"`
}

func (h *Handler) viewInvitation(i accounts.Invitation) invitationView {
	status := "pending"
	if i.Expired(h.acc.Now()) {
		status = "expired"
	}
	return invitationView{ID: i.ID, Email: i.Email, Role: i.Role, Status: status, InvitedByEmail: i.InvitedByEmail,
		CreatedAt: i.CreatedAt.Unix(), ExpiresAt: i.ExpiresAt.Unix()}
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	list, err := h.acc.ListInvitations(r.Context(), u.AccountID)
	if err != nil {
		h.internal(w, "list invitations", err)
		return
	}
	out := make([]invitationView, len(list))
	for i, inv := range list {
		out[i] = h.viewInvitation(inv)
	}
	writeJSON(w, http.StatusOK, out)
}

type inviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *Handler) invite(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.fresh(w, s) {
		return
	}
	var req inviteRequest
	if !decode(w, r, &req) {
		return
	}
	email := accounts.NormalizeEmail(req.Email)
	if err := accounts.ValidateEmail(email); err != nil {
		writeAccountsError(w, accounts.ErrInvalidEmail)
		return
	}
	if req.Role != accounts.RoleAdmin && req.Role != accounts.RoleMember {
		writeAccountsError(w, accounts.ErrInviteRole)
		return
	}
	if !accounts.CanInviteAs(u.Role, req.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.hit(w, r, accounts.InvitesPerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	inv, token, err := h.acc.Invite(r.Context(), u.AccountID, u.ID, email, req.Role)
	if err != nil {
		writeErr(h, w, "invite", err)
		return
	}
	h.sendInvitation(w, r, u, inv, token, false)
}

// resendInvitation is "Send again": the old link stops working and a new
// one, valid for another 7 days, goes to the same address.
func (h *Handler) resendInvitation(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	ctx := r.Context()
	old, err := h.acc.InvitationByID(ctx, u.AccountID, r.PathValue("id"))
	if err != nil {
		writeErr(h, w, "load invitation", err)
		return
	}
	if !accounts.CanInviteAs(u.Role, old.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if !h.fresh(w, s) {
		return
	}
	if !h.hit(w, r, accounts.InvitesPerAccount, strconv.FormatInt(u.AccountID, 10)) {
		return
	}
	inv, token, err := h.acc.ReissueInvitation(ctx, u.AccountID, u.ID, old.ID)
	if err != nil {
		writeErr(h, w, "reissue invitation", err)
		return
	}
	h.sendInvitation(w, r, u, inv, token, true)
}

// sendInvitation emails the link before answering: an invitation whose
// email could not be sent is useless, and the inviter can try again.
func (h *Handler) sendInvitation(w http.ResponseWriter, r *http.Request, u accounts.User, inv accounts.Invitation, token string, resent bool) {
	ctx := r.Context()
	a, err := h.acc.AccountByID(ctx, u.AccountID)
	if err != nil {
		h.internal(w, "load account", err)
		return
	}
	detail := map[string]any{"invitation_id": inv.ID, "role": inv.Role}
	if resent {
		detail["resent"] = true
	}
	h.audit(ctx, accounts.ActionInvited, clientInfo(r), u.ID, u.AccountID, detail)
	if err := h.mail.Invitation(ctx, inv.Email, mailer.Invite{AccountName: a.Name, InviterEmail: u.Email, Role: inv.Role,
		Token: token, ExpiresAt: inv.ExpiresAt}); err != nil {
		h.log.Error("invitation email failed", "account", u.AccountID, "invitation", inv.ID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "email_unavailable", "email_unavailable",
			"The invitation was saved but we couldn't send its email just now. Use Send again in a few minutes.", "")
		return
	}
	writeJSON(w, http.StatusCreated, h.viewInvitation(inv))
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
	if !accounts.CanManage(u.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	ctx := r.Context()
	inv, err := h.acc.InvitationByID(ctx, u.AccountID, r.PathValue("id"))
	if err != nil {
		writeErr(h, w, "load invitation", err)
		return
	}
	if !accounts.CanInviteAs(u.Role, inv.Role) {
		writeAccountsError(w, accounts.ErrRoleRequired)
		return
	}
	if _, err := h.acc.RevokeInvitation(ctx, u.AccountID, inv.ID); err != nil {
		writeErr(h, w, "revoke invitation", err)
		return
	}
	h.audit(ctx, accounts.ActionInviteRevoked, clientInfo(r), u.ID, u.AccountID, map[string]any{"invitation_id": inv.ID, "role": inv.Role})
	w.WriteHeader(http.StatusNoContent)
}

type tokenRequest struct {
	Token string `json:"token"`
}

// lookupInvitation is what the accept page shows. It takes the token in
// the body, not the path, so it never lands in an access log. The token is
// 32 random bytes, so like a reset token it needs no throttle.
func (h *Handler) lookupInvitation(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.acc.PeekInvitation(r.Context(), req.Token)
	if errors.Is(err, accounts.ErrInvitationInvalid) {
		invitationGone(w)
		return
	}
	if err != nil {
		h.internal(w, "look up invitation", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_name": p.AccountName, "email": p.Email, "role": p.Role,
		"invited_by_email": p.InvitedByEmail, "expires_at": p.ExpiresAt.Unix()})
}

func invitationGone(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "not_found",
		"This invitation has expired, was withdrawn, or was already used. Ask the person who invited you to send it again.", "")
}

type acceptRequest struct {
	Token           string `json:"token"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
}

// acceptInvitation creates the invited user and logs it in. No verification
// code: the link in the email proves the address. It counts against the
// signup throttle, since it creates a login.
func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	p, err := h.acc.PeekInvitation(ctx, req.Token)
	if errors.Is(err, accounts.ErrInvitationInvalid) {
		invitationGone(w)
		return
	}
	if err != nil {
		h.internal(w, "look up invitation", err)
		return
	}
	if err := accounts.ValidateNewPassword(p.Email, req.Password, req.PasswordConfirm); err != nil {
		var ae *accounts.Error
		errors.As(err, &ae)
		writeAccountsError(w, ae)
		return
	}
	c := clientInfo(r)
	if !h.hit(w, r, accounts.SignupPerIP, c.IP) {
		return
	}
	hash, err := h.acc.HashPassword(ctx, req.Password)
	if err != nil {
		h.internal(w, "hash password", err)
		return
	}
	u, p, err := h.acc.AcceptInvitation(ctx, req.Token, hash)
	switch {
	case errors.Is(err, accounts.ErrInvitationInvalid):
		invitationGone(w)
		return
	case err != nil:
		writeErr(h, w, "accept invitation", err)
		return
	}
	h.audit(ctx, accounts.ActionInviteAccepted, c, u.ID, u.AccountID, map[string]any{"invitation_id": p.ID, "role": u.Role})
	if p.InvitedByEmail != "" {
		msg := mailer.Joined{Email: u.Email, AccountName: p.AccountName, Role: u.Role, At: h.now()}
		inviter := p.InvitedByEmail
		h.sendLater("invitation accepted email", func(ctx context.Context) error {
			return h.mail.InvitationAccepted(ctx, inviter, msg)
		})
	}
	h.startSession(w, r, u)
}
