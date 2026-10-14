// Package dashboard serves the dashboard API, /dashboard/v1 on the app host
// (ADR-0011 §5 and §8, docs/spec/accounts.md "Dashboard API"): signup,
// verification, login, sessions, password reset and change, API keys, the
// balance and its history, monthly usage, the account's jobs, card top-ups
// and the Stripe webhook that credits them. It is mounted
// only with ACCOUNTS_BACKEND=local; with the console backend compute-api
// registers none of it.
//
// What every request passes, in order:
//
//  1. Host must be the APP_ORIGIN host, else 404. The ALB sends only that
//     host's /dashboard/v1/* here, but compute-api also answers the API
//     host, and the session cookie must never be usable there.
//  2. A non-GET must carry Origin equal to APP_ORIGIN, else 403
//     csrf_failed. Browsers send Origin on every cross-origin and every
//     same-origin POST, so a missing one is refused, not waved through.
//  3. Session routes need the __Host-uc_session cookie, and a non-GET also
//     needs X-CSRF-Token equal to the session's token.
//
// The one exception to 2 and 3 is POST /stripe/webhook, which Stripe calls
// with neither; it is authenticated by its Stripe-Signature alone.
//
// No API key is read here and no cookie is read on /compute/v1.
package dashboard

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/listings"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ratelimit"
)

const (
	// Prefix is the path every dashboard route lives under.
	Prefix = "/dashboard/v1"
	// CookieName is host-only by its __Host- prefix: Secure, Path=/, no
	// Domain, so no other host under unifyai.us can set or read it.
	CookieName = "__Host-uc_session"
	// CSRFHeader carries the csrf_token from login, verify or /me.
	CSRFHeader = "X-CSRF-Token"

	maxBody      = 16 << 10
	maxUserAgent = 512
	// mailTimeout bounds a message sent after the response (forgot,
	// resend, password changed).
	mailTimeout = 30 * time.Second
)

type Config struct {
	Accounts *accounts.Service
	Mailer   *mailer.Mailer
	// AppOrigin is APP_ORIGIN, e.g. https://app.compute.unifyai.us. http is
	// accepted only for a loopback host, for local development.
	AppOrigin string
	Log       *slog.Logger
	// Now dates the password-changed email and picks the current month for
	// /usage; nil is time.Now. Session and token times come from the
	// accounts service's own clock.
	Now func() time.Time
	// Jobs serves /jobs and its sub-routes with the job API's own handlers
	// (api.Server.AccountJobs). Ledger reads the balance history and usage
	// (billing.Local). Either nil answers its routes 501 not_implemented;
	// main sets both.
	Jobs JobRoutes
	// Market serves GET /templates, GET /skus and POST /jobs with the job
	// API's own handlers (api.Server.AccountJobs too); nil answers them 501.
	Market MarketRoutes
	// Listings is the template marketplace's account side (M2); nil answers
	// its routes 501 not_implemented.
	Listings *listings.Store
	Ledger   Ledger
	// Payments serves /topups and the Stripe webhook (internal/payments).
	// Nil answers them 503 topups_unavailable; main sets it with
	// ACCOUNTS_BACKEND=local, with or without Stripe keys.
	Payments *payments.Service
}

type Handler struct {
	acc      *accounts.Service
	mail     *mailer.Mailer
	origin   string
	host     string
	log      *slog.Logger
	now      func() time.Time
	jobs     JobRoutes
	mkt      MarketRoutes
	ledger   Ledger
	listings *listings.Store
	pay      *payments.Service
	reads    *ratelimit.Limiter

	mux      *http.ServeMux
	patterns []string

	mailWG sync.WaitGroup
}

func New(cfg Config) (*Handler, error) {
	if cfg.Accounts == nil || cfg.Mailer == nil || cfg.Log == nil {
		return nil, errors.New("dashboard: accounts, mailer and log are required")
	}
	origin := strings.TrimRight(strings.TrimSpace(cfg.AppOrigin), "/")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil ||
		!(u.Scheme == "https" || u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("APP_ORIGIN %q: the dashboard needs the app's origin, e.g. https://app.compute.unifyai.us (http only for localhost)", cfg.AppOrigin)
	}
	h := &Handler{acc: cfg.Accounts, mail: cfg.Mailer, origin: strings.ToLower(origin), host: strings.ToLower(u.Host),
		log: cfg.Log, now: cfg.Now, jobs: cfg.Jobs, mkt: cfg.Market, ledger: cfg.Ledger, listings: cfg.Listings, pay: cfg.Payments, reads: ratelimit.New(readBurst, readWindow), mux: http.NewServeMux()}
	if h.now == nil {
		h.now = time.Now
	}
	h.handle("GET /config", h.config)
	h.handle("POST /signup", h.signup)
	h.handle("POST /verify", h.verify)
	h.handle("POST /verify/resend", h.resend)
	h.handle("POST /login", h.login)
	h.handle("POST /password/forgot", h.forgot)
	h.handle("POST /password/reset", h.reset)
	h.handle("GET /me", h.session(h.me))
	h.handle("POST /logout", h.session(h.logout))
	h.handle("POST /password", h.session(h.changePassword))
	h.handle("POST /reauth", h.session(h.reauth))
	h.handle("GET /sessions", h.session(h.read(h.sessions)))
	h.handle("POST /sessions/end-others", h.session(h.endOtherSessions))
	h.handle("GET /keys", h.session(h.listKeys))
	h.handle("POST /keys", h.session(h.createKey))
	h.handle("DELETE /keys/{id}", h.session(h.revokeKey))
	h.handle("PATCH /keys/{id}", h.session(h.updateKey))
	h.handle("GET /listings", h.session(h.read(h.listListings)))
	h.handle("POST /listings", h.session(h.createListing))
	h.handle("PATCH /listings/{id}", h.session(h.updateListing))
	h.handle("POST /listings/{id}/unlist", h.session(h.unlistListing))
	h.handle("GET /balance", h.session(h.read(h.balance)))
	h.handle("GET /transactions", h.session(h.read(h.transactions)))
	h.handle("GET /usage", h.session(h.read(h.usage)))
	h.handle("GET /usage/jobs", h.session(h.read(h.usageJobs)))
	h.handle("GET /usage/export", h.session(h.read(h.usageExport)))
	h.handle("GET /templates", h.session(h.read(h.market(MarketRoutes.Templates))))
	h.handle("GET /skus", h.session(h.read(h.market(MarketRoutes.SKUs))))
	h.handle("POST /jobs", h.session(h.market(MarketRoutes.Submit)))
	h.handle("GET /jobs", h.session(h.read(h.job(JobRoutes.List))))
	h.handle("GET /jobs/{id}", h.session(h.read(h.job(JobRoutes.Get))))
	h.handle("POST /jobs/{id}/cancel", h.session(h.job(JobRoutes.Cancel)))
	h.handle("GET /jobs/{id}/events", h.session(h.read(h.job(JobRoutes.Events))))
	h.handle("GET /jobs/{id}/logs", h.session(h.read(h.job(JobRoutes.Logs))))
	h.handle("GET /jobs/{id}/artifacts", h.session(h.read(h.job(JobRoutes.Artifacts))))
	h.handle("POST /topups", h.session(h.createTopup))
	h.handle("GET /topups", h.session(h.read(h.listTopups)))
	h.handle("GET /topups/{id}", h.session(h.read(h.getTopup)))
	h.handle("POST "+webhookPath, h.stripeWebhook)
	h.mux.HandleFunc(Prefix+"/", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
	return h, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func (h *Handler) handle(pattern string, f http.HandlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	full := method + " " + Prefix + path
	h.patterns = append(h.patterns, full)
	h.mux.HandleFunc(full, f)
}

// Patterns lists the registered routes as "METHOD /path", for the test that
// holds them against docs/spec/dashboard-openapi.json.
func (h *Handler) Patterns() []string { return append([]string(nil), h.patterns...) }

// Wait blocks until every email queued after a response has been handed to
// the transport (or failed). main calls it at shutdown; tests before reading
// the capture.
func (h *Handler) Wait() { h.mailWG.Wait() }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Bodies carry csrf tokens and account data; nothing may cache them.
	w.Header().Set("Cache-Control", "no-store")
	if !strings.EqualFold(r.Host, h.host) {
		notFound(w)
		return
	}
	// Stripe's webhook is a server-to-server POST with no Origin and no
	// session; its Stripe-Signature is its credential (topups.go).
	isWebhook := r.Method == http.MethodPost && r.URL.Path == Prefix+webhookPath
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !isWebhook && !strings.EqualFold(r.Header.Get("Origin"), h.origin) {
		csrfFailed(w)
		return
	}
	h.mux.ServeHTTP(w, r)
}

// clientIP is the address throttles and audit rows record. compute-api sits
// directly behind the ALB, which appends the connecting address to
// X-Forwarded-For, so the client is the LAST entry. The first is whatever
// the client chose to send (Unify Data keyed its limits on it, and one
// header defeated them). Without the header (local runs) it is the peer.
func clientIP(r *http.Request) string {
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		last := xff[len(xff)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
			return a.Unmap().String()
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().String()
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

func clientInfo(r *http.Request) accounts.ClientInfo {
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	return accounts.ClientInfo{IP: clientIP(r), UserAgent: ua}
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, s accounts.Session, u accounts.User)

// session resolves the cookie to a live session (idle and absolute limits
// are the accounts service's) and, for a non-GET, checks the CSRF header.
// A cookie that no longer names a session is cleared, so the page stops
// sending it.
func (h *Handler) session(next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(CookieName); err == nil {
			token = c.Value
		}
		sess, u, err := h.acc.LookupSession(r.Context(), token)
		if errors.Is(err, accounts.ErrSessionRequired) {
			if token != "" {
				clearCookie(w)
			}
			writeAccountsError(w, accounts.ErrSessionRequired)
			return
		}
		if err != nil {
			h.internal(w, "session lookup", err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(sess.CSRFToken)) != 1 {
			csrfFailed(w)
			return
		}
		next(w, r, sess, u)
	}
}

func setCookie(w http.ResponseWriter, s accounts.Session, now time.Time) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: s.Token, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: max(int(s.ExpiresAt.Sub(now)/time.Second), 1)})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// sendLater hands a message to the mailer after the response. Forgot and
// resend use it so the response time does not depend on whether the
// address exists (a send to SES takes far longer than the lookup that
// decides there is nothing to send).
func (h *Handler) sendLater(what string, f func(ctx context.Context) error) {
	h.mailWG.Add(1)
	go func() {
		defer h.mailWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		defer cancel()
		if err := f(ctx); err != nil {
			h.log.Error("dashboard background step failed", "step", what, "err", err)
		}
	}()
}

func (h *Handler) audit(ctx context.Context, action string, c accounts.ClientInfo, userID, accountID int64, detail map[string]any) {
	actor := ""
	if userID != 0 {
		actor = strconv.FormatInt(userID, 10)
	}
	if _, err := h.acc.Audit(ctx, accounts.AuditEvent{ActorType: accounts.ActorUser, ActorID: actor, AccountID: accountID,
		Action: action, Client: c, Detail: detail}); err != nil {
		h.log.Error("audit write failed", "action", action, "account", accountID, "err", err)
	}
}

// hit counts an attempt against rule and answers 429 when it is over.
func (h *Handler) hit(w http.ResponseWriter, r *http.Request, rule accounts.Rule, subject string) bool {
	d, err := h.acc.Hit(r.Context(), rule, subject)
	return h.allowed(w, rule, d, err)
}

// check answers 429 if one more attempt would be over rule, without
// counting; for limits on failures, which hit only when one happens.
func (h *Handler) check(w http.ResponseWriter, r *http.Request, rule accounts.Rule, subject string) bool {
	d, err := h.acc.Check(r.Context(), rule, subject)
	return h.allowed(w, rule, d, err)
}

func (h *Handler) allowed(w http.ResponseWriter, rule accounts.Rule, d accounts.Decision, err error) bool {
	if err != nil {
		h.internal(w, "throttle "+rule.Bucket, err)
		return false
	}
	if !d.Allowed {
		secs := int64(d.RetryAfter / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "rate_limited",
			fmt.Sprintf("Too many attempts. Try again in %s.", humanWait(secs)), "")
		return false
	}
	return true
}

func humanWait(secs int64) string {
	switch {
	case secs < 60:
		return strconv.FormatInt(secs, 10) + " seconds"
	case secs < 120:
		return "a minute"
	default:
		return strconv.FormatInt((secs+59)/60, 10) + " minutes"
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed_body", "Body must be a JSON object.", "")
		return false
	}
	return true
}

type apiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, typ, code, msg, param string) {
	writeJSON(w, status, map[string]apiError{"error": {Type: typ, Code: code, Message: msg, Param: param}})
}

// statusOf maps the accounts package's codes onto spec/accounts.md "Errors".
var statusOf = map[string]struct {
	status int
	typ    string
}{
	"invalid_email":       {http.StatusBadRequest, "invalid_request"},
	"weak_password":       {http.StatusBadRequest, "invalid_request"},
	"password_mismatch":   {http.StatusBadRequest, "invalid_request"},
	"invalid_code":        {http.StatusBadRequest, "invalid_request"},
	"invalid_token":       {http.StatusBadRequest, "invalid_request"},
	"invalid_credentials": {http.StatusUnauthorized, "authentication_error"},
	"session_required":    {http.StatusUnauthorized, "authentication_error"},
	"email_unverified":    {http.StatusForbidden, "authentication_error"},
	"account_disabled":    {http.StatusForbidden, "authentication_error"},
	"reauth_required":     {http.StatusUnauthorized, "authentication_error"},
	"invalid_name":        {http.StatusBadRequest, "invalid_request"},
	"invalid_scope":       {http.StatusBadRequest, "invalid_request"},
	"invalid_expires_at":  {http.StatusBadRequest, "invalid_request"},
	"account_suspended":   {http.StatusForbidden, "authentication_error"},
	"key_limit_reached":   {http.StatusForbidden, "permission_error"},
	"invalid_cap":         {http.StatusBadRequest, "invalid_request"},
	"compute_not_enabled": {http.StatusForbidden, "permission_error"},
}

func writeAccountsError(w http.ResponseWriter, e *accounts.Error) {
	s, ok := statusOf[e.Code]
	if !ok {
		s.status, s.typ = http.StatusBadRequest, "invalid_request"
	}
	writeError(w, s.status, s.typ, e.Code, e.Message, e.Param)
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "not_found", "Not found.", "")
}

func csrfFailed(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "permission_error", "csrf_failed",
		"This request did not come from the Unify Compute dashboard. Reload the page and try again.", "")
}

func botCheckFailed(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "bot_check_failed",
		"We couldn't confirm this request came from a person. Reload the page and try again.", "")
}

func (h *Handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error("dashboard request failed", "step", what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal_error", "Something went wrong on our side. Try again.", "")
}
