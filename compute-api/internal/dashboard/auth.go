package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

// Throttles beyond A1's (ADR-0011 D5: with no Turnstile, per-IP and
// per-address limits are the bot defence). Keyed on the normalised address
// whether or not it is registered, so a 429 says nothing about existence.
var (
	resendPerIP    = accounts.Rule{Bucket: "resend_ip", Limit: 20, Window: time.Hour}
	forgotPerEmail = accounts.Rule{Bucket: "forgot_email", Limit: 5, Window: time.Hour}
)

// Login throttling. Per IP every attempt counts, success included: the
// limit exists to bound the argon2id work one address can make us do, and
// a successful hash costs as much as a failed one. Per address only
// failures count (Check before, Hit on failure), so the owner's own logins
// never use up the backstop. That rule is keyed on the normalised email,
// not the account id, so an unregistered address is throttled exactly like
// a registered one and a 429 is no enumeration oracle. The same per-address
// rule bounds wrong current passwords on /password and /reauth, so a stolen
// session cannot be used to guess the password faster than the login form.
var (
	loginPerIP      = accounts.LoginPerIP
	passwordPerUser = accounts.LoginPerAccount
)

// honeypot is the `website` field the pages hide from people (ADR-0011 D5,
// copied from Unify Data's botcheck). A script that fills every field fills
// it too.
type honeypot struct {
	Website string `json:"website"`
}

func (p honeypot) tripped() bool { return p.Website != "" }

type userView struct {
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

type accountView struct {
	ID             int64  `json:"id"`
	Status         string `json:"status"`
	ComputeEnabled bool   `json:"compute_enabled"`
}

type sessionView struct {
	User      userView    `json:"user"`
	Account   accountView `json:"account"`
	CSRFToken string      `json:"csrf_token"`
}

func (h *Handler) sessionBody(ctx context.Context, u accounts.User, csrf string) (sessionView, error) {
	a, err := h.acc.AccountByID(ctx, u.AccountID)
	if err != nil {
		return sessionView{}, err
	}
	return sessionView{
		User:      userView{Email: u.Email, CreatedAt: u.CreatedAt.Unix()},
		Account:   accountView{ID: a.ID, Status: a.Status, ComputeEnabled: a.ComputeEnabled},
		CSRFToken: csrf,
	}, nil
}

// startSession rotates: any session the browser already holds is ended
// before a new one is issued (login and verify are privilege changes).
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, u accounts.User) {
	ctx := r.Context()
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if err := h.acc.DeleteSession(ctx, c.Value); err != nil {
			h.internal(w, "end previous session", err)
			return
		}
	}
	sess, err := h.acc.CreateSession(ctx, u.ID, clientInfo(r))
	if err != nil {
		h.internal(w, "create session", err)
		return
	}
	body, err := h.sessionBody(ctx, u, sess.CSRFToken)
	if err != nil {
		h.internal(w, "load account", err)
		return
	}
	setCookie(w, sess, sess.CreatedAt)
	writeJSON(w, http.StatusOK, body)
}

func (h *Handler) config(w http.ResponseWriter, _ *http.Request) {
	// turnstile_site_key stays in the shape spec/accounts.md gives it, null
	// while Turnstile is off (ADR-0011 D5), so the pages render no widget.
	// The top-up range is the accounts table's default (D4).
	writeJSON(w, http.StatusOK, map[string]any{
		"turnstile_site_key":  nil,
		"min_password_length": accounts.MinPasswordLength,
		"topup":               map[string]string{"min_usd": "10.00", "max_usd": "100.00"},
	})
}

type signupRequest struct {
	honeypot
	Email           string `json:"email"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
}

// signup answers 202 for every well-formed request. A new address gets a
// code; a registered one gets the "someone tried to sign up" email, never a
// 409 (Unify Data's email_taken is the enumeration we do not copy). Both
// paths hash the password and send one message synchronously, so they take
// about the same time.
func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !decode(w, r, &req) {
		return
	}
	if req.tripped() {
		botCheckFailed(w)
		return
	}
	email := accounts.NormalizeEmail(req.Email)
	if err := accounts.ValidateEmail(email); err != nil {
		writeAccountsError(w, accounts.ErrInvalidEmail)
		return
	}
	if err := accounts.ValidateNewPassword(email, req.Password, req.PasswordConfirm); err != nil {
		var ae *accounts.Error
		errors.As(err, &ae)
		writeAccountsError(w, ae)
		return
	}
	// Counted after validation, so a typo does not use up the hour.
	c := clientInfo(r)
	if !h.hit(w, r, accounts.SignupPerIP, c.IP) {
		return
	}
	ctx := r.Context()
	hash, err := h.acc.HashPassword(ctx, req.Password)
	if err != nil {
		h.internal(w, "hash password", err)
		return
	}
	u, a, err := h.acc.CreatePending(ctx, email, hash)
	if errors.Is(err, accounts.ErrEmailTaken) {
		existing, err := h.acc.UserByEmail(ctx, email)
		if err != nil && !errors.Is(err, accounts.ErrNotFound) {
			h.internal(w, "look up registered address", err)
			return
		}
		if err := h.mail.SignupAttempt(ctx, email); err != nil {
			// Not shown: a 503 here and a 202 for a new address would say
			// which one this is.
			h.log.Error("signup attempt email failed", "account", existing.AccountID, "err", err)
		}
		h.audit(ctx, accounts.ActionSignup, c, existing.ID, existing.AccountID, map[string]any{"outcome": "address_registered"})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_sent"})
		return
	}
	if err != nil {
		h.internal(w, "create account", err)
		return
	}
	code, err := h.acc.IssueVerifyCode(ctx, u.ID)
	if err == nil {
		err = h.mail.VerificationCode(ctx, email, code)
	}
	if err != nil {
		h.log.Error("verification code not sent; rolling back signup", "account", a.ID, "err", err)
		if err := h.acc.DeletePending(context.WithoutCancel(ctx), u.ID); err != nil {
			h.log.Error("signup rollback failed", "account", a.ID, "err", err)
		}
		writeError(w, http.StatusServiceUnavailable, "email_unavailable", "email_unavailable",
			"We couldn't send the verification email just now, so no account was created. Try again in a few minutes.", "")
		return
	}
	h.audit(ctx, accounts.ActionSignup, c, u.ID, a.ID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_sent"})
}

type verifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// verify activates a pending user and logs them in. Only wrong codes count
// against the per-IP hour (the per-code limit of 5 is the accounts
// service's).
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decode(w, r, &req) {
		return
	}
	c := clientInfo(r)
	if !h.check(w, r, accounts.VerifyPerIP, c.IP) {
		return
	}
	u, err := h.acc.VerifyEmail(r.Context(), req.Email, strings.ReplaceAll(strings.TrimSpace(req.Code), " ", ""))
	if errors.Is(err, accounts.ErrInvalidCode) {
		if _, err := h.acc.Hit(r.Context(), accounts.VerifyPerIP, c.IP); err != nil {
			h.internal(w, "throttle verify", err)
			return
		}
		writeAccountsError(w, accounts.ErrInvalidCode)
		return
	}
	if err != nil {
		h.internal(w, "verify email", err)
		return
	}
	h.audit(r.Context(), accounts.ActionVerify, c, u.ID, u.AccountID, nil)
	h.startSession(w, r, u)
}

type emailRequest struct {
	honeypot
	Email string `json:"email"`
}

// decodeEmail is the shared front of resend and forgot.
func decodeEmail(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req emailRequest
	if !decode(w, r, &req) {
		return "", false
	}
	if req.tripped() {
		botCheckFailed(w)
		return "", false
	}
	email := accounts.NormalizeEmail(req.Email)
	if err := accounts.ValidateEmail(email); err != nil {
		writeAccountsError(w, accounts.ErrInvalidEmail)
		return "", false
	}
	return email, true
}

// resend always answers 202. The lookup and the send happen after the
// response, so neither the status nor the time says whether the address is
// pending, active or unknown.
func (h *Handler) resend(w http.ResponseWriter, r *http.Request) {
	email, ok := decodeEmail(w, r)
	if !ok {
		return
	}
	// Minute before hour: a refused click inside the minute does not use
	// up one of the hour's five.
	if !h.hit(w, r, resendPerIP, clientIP(r)) || !h.hit(w, r, accounts.ResendPerEmailMinute, email) ||
		!h.hit(w, r, accounts.ResendPerEmailHour, email) {
		return
	}
	h.sendLater("resend verification code", func(ctx context.Context) error {
		u, err := h.acc.UserByEmail(ctx, email)
		if errors.Is(err, accounts.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		code, err := h.acc.IssueVerifyCode(ctx, u.ID)
		if errors.Is(err, accounts.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return h.mail.VerificationCode(ctx, email, code)
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_sent"})
}

type loginRequest struct {
	honeypot
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	if req.tripped() {
		botCheckFailed(w)
		return
	}
	email := accounts.NormalizeEmail(req.Email)
	if err := accounts.ValidateEmail(email); err != nil {
		writeAccountsError(w, accounts.ErrInvalidEmail)
		return
	}
	c := clientInfo(r)
	if !h.hit(w, r, loginPerIP, c.IP) || !h.check(w, r, passwordPerUser, email) {
		return
	}
	ctx := r.Context()
	u, err := h.acc.Authenticate(ctx, email, req.Password)
	var ae *accounts.Error
	if errors.As(err, &ae) {
		if ae == accounts.ErrInvalidCredentials {
			if _, err := h.acc.Hit(ctx, passwordPerUser, email); err != nil {
				h.internal(w, "throttle login", err)
				return
			}
		}
		// The account is named only when the address is registered; an
		// unknown address is recorded by IP alone.
		known, _ := h.acc.UserByEmail(ctx, email)
		h.audit(ctx, accounts.ActionLoginFailed, c, known.ID, known.AccountID, map[string]any{"reason": ae.Code})
		writeAccountsError(w, ae)
		return
	}
	if err != nil {
		h.internal(w, "authenticate", err)
		return
	}
	h.audit(ctx, accounts.ActionLogin, c, u.ID, u.AccountID, nil)
	h.startSession(w, r, u)
}

// forgot always answers 202 and does its work after the response, like
// resend. Pending users get a link too: following it proves the address.
func (h *Handler) forgot(w http.ResponseWriter, r *http.Request) {
	email, ok := decodeEmail(w, r)
	if !ok {
		return
	}
	c := clientInfo(r)
	if !h.hit(w, r, accounts.ForgotPerIP, c.IP) || !h.hit(w, r, forgotPerEmail, email) {
		return
	}
	h.sendLater("password reset", func(ctx context.Context) error {
		u, err := h.acc.UserByEmail(ctx, email)
		if errors.Is(err, accounts.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		token, err := h.acc.IssueResetToken(ctx, u.ID)
		if errors.Is(err, accounts.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		h.audit(ctx, accounts.ActionResetRequested, c, u.ID, u.AccountID, nil)
		return h.mail.PasswordReset(ctx, email, token)
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "reset_requested"})
}

type resetRequest struct {
	Token           string `json:"token"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
}

// reset sets a new password from an emailed link, ends every session of the
// user (this browser's included, so its cookie is cleared) and does not log
// in. The token is 32 random bytes, so it needs no throttle: guessing is
// hopeless and an unknown token costs one indexed read, never a hash.
func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Password != req.PasswordConfirm {
		writeAccountsError(w, accounts.ErrPasswordMismatch)
		return
	}
	ctx := r.Context()
	u, err := h.acc.ResetTokenUser(ctx, req.Token)
	if errors.Is(err, accounts.ErrInvalidToken) {
		writeAccountsError(w, accounts.ErrInvalidToken)
		return
	}
	if err != nil {
		h.internal(w, "look up reset token", err)
		return
	}
	if err := accounts.ValidatePassword(u.Email, req.Password); err != nil {
		writeAccountsError(w, accounts.ErrWeakPassword)
		return
	}
	hash, err := h.acc.HashPassword(ctx, req.Password)
	if err != nil {
		h.internal(w, "hash password", err)
		return
	}
	u, err = h.acc.ResetPassword(ctx, req.Token, hash)
	if errors.Is(err, accounts.ErrInvalidToken) {
		writeAccountsError(w, accounts.ErrInvalidToken)
		return
	}
	if err != nil {
		h.internal(w, "reset password", err)
		return
	}
	h.audit(ctx, accounts.ActionPasswordReset, clientInfo(r), u.ID, u.AccountID, nil)
	at := h.now()
	h.sendLater("password changed (reset)", func(ctx context.Context) error {
		return h.mail.PasswordChanged(ctx, u.Email, at)
	})
	clearCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_reset"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	body, err := h.sessionBody(r.Context(), u, s.CSRFToken)
	if err != nil {
		h.internal(w, "load account", err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	if err := h.acc.DeleteSession(r.Context(), s.Token); err != nil {
		h.internal(w, "end session", err)
		return
	}
	h.audit(r.Context(), accounts.ActionLogout, clientInfo(r), u.ID, u.AccountID, nil)
	clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

var errWrongCurrent = &accounts.Error{Code: "invalid_credentials", Param: "current_password", Message: "Your current password is incorrect."}

// checkPassword is the re-entered password of /password and /reauth, under
// the same per-address failure limit as login.
func (h *Handler) checkPassword(w http.ResponseWriter, r *http.Request, u accounts.User, password, via string, wrong *accounts.Error) bool {
	if !h.check(w, r, passwordPerUser, u.Email) {
		return false
	}
	ctx := r.Context()
	err := h.acc.VerifyPassword(ctx, u.ID, password)
	if errors.Is(err, accounts.ErrInvalidCredentials) {
		if _, err := h.acc.Hit(ctx, passwordPerUser, u.Email); err != nil {
			h.internal(w, "throttle "+via, err)
			return false
		}
		h.audit(ctx, accounts.ActionLoginFailed, clientInfo(r), u.ID, u.AccountID, map[string]any{"reason": "invalid_credentials", "via": via})
		writeAccountsError(w, wrong)
		return false
	}
	if err != nil {
		h.internal(w, "verify password", err)
		return false
	}
	return true
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
}

// changePassword needs the current password in the body; that is the
// "recent password" ADR-0011 §8 requires, so a session older than the
// reauth window can still change it, and doing so counts as a reauth.
// Every other session of the user ends.
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	var req changePasswordRequest
	if !decode(w, r, &req) {
		return
	}
	if err := accounts.ValidateNewPassword(u.Email, req.Password, req.PasswordConfirm); err != nil {
		var ae *accounts.Error
		errors.As(err, &ae)
		writeAccountsError(w, ae)
		return
	}
	if !h.checkPassword(w, r, u, req.CurrentPassword, "password_change", errWrongCurrent) {
		return
	}
	ctx := r.Context()
	hash, err := h.acc.HashPassword(ctx, req.Password)
	if err != nil {
		h.internal(w, "hash password", err)
		return
	}
	if err := h.acc.ChangePassword(ctx, u.ID, hash, s.Token); err != nil {
		h.internal(w, "change password", err)
		return
	}
	if err := h.acc.MarkReauth(ctx, s.Token); err != nil {
		h.internal(w, "mark reauth", err)
		return
	}
	h.audit(ctx, accounts.ActionPasswordChanged, clientInfo(r), u.ID, u.AccountID, nil)
	at := h.now()
	h.sendLater("password changed", func(ctx context.Context) error {
		return h.mail.PasswordChanged(ctx, u.Email, at)
	})
	w.WriteHeader(http.StatusNoContent)
}

type reauthRequest struct {
	Password string `json:"password"`
}

var errWrongReauth = &accounts.Error{Code: "invalid_credentials", Param: "password", Message: "Incorrect password."}

// reauth records that the password was just entered, for the actions that
// need one within accounts.ReauthWindow (key creation, A5).
func (h *Handler) reauth(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User) {
	var req reauthRequest
	if !decode(w, r, &req) {
		return
	}
	if !h.checkPassword(w, r, u, req.Password, "reauth", errWrongReauth) {
		return
	}
	if err := h.acc.MarkReauth(r.Context(), s.Token); err != nil {
		h.internal(w, "mark reauth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
