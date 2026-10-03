// Package consoletest is an in-process fake of the console's side of the
// billing bridge, written from docs/spec/console-billing-bridge.md alone. It
// exists so compute-api tests can exercise holds, settlement and console
// outages without a running console. It follows the spec as revised on
// 2026-10-01 (PR #11): extend_id on extend, the error-code table carried in
// `message`, a bodiless 404 when the bridge is disabled, 403 with data.status
// for suspended tenants and disabled users, and a dedicated rate limiter.
// From the 2026-10-02 revision it implements 413 payload_too_large (checked
// before the signature) and internal_error on a forced 500. It does NOT yet
// refuse a settle without sku as 400 invalid_request: it answers 409
// sku_mismatch, which an existing test pins and only the operator may change.
//
// Nothing here is derived from FelixSphere/unifyapi-console (AGPL-3.0). Where
// the spec is silent, the behaviour chosen is documented on the method or
// constant that implements it, so a disagreement with the real console shows
// up as a named assumption rather than a surprise.
package consoletest

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
)

// QuotaPerUnit is the console's quota per USD 1.
const QuotaPerUnit = 500000

// MaxSkew is the replay window. The spec rejects requests older than 30 s; it
// does not mention future timestamps, and the fake rejects those beyond the
// same window too, since a clock that far ahead is as wrong as one behind.
const MaxSkew = 30 * time.Second

const (
	RouteVerify  = "verify"
	RouteHold    = "hold"
	RouteSettle  = "settle"
	RouteExtend  = "extend"
	RouteRelease = "release"
)

// Error codes, from the spec's table under "Errors and 404". Each is sent in
// `message`, which clients read, and repeated in the optional `code` field.
const (
	CodeInvalidRequest           = "invalid_request"
	CodeInvalidSignature         = "invalid_signature"
	CodeInvalidTimestamp         = "invalid_timestamp"
	CodeInvalidKey               = "invalid_key"
	CodeDisabled                 = "disabled"
	CodeSuspended                = "suspended"
	CodeInsufficientBalance      = "insufficient_balance"
	CodeHoldNotFound             = "hold_not_found"
	CodeHoldExceeded             = "hold_exceeded"
	CodeHoldExpired              = "hold_expired"
	CodeHoldClosed               = "hold_closed"
	CodeIdempotencyConflict      = "idempotency_conflict"
	CodeRateLimited              = "rate_limited"
	CodeSKUMismatch              = "sku_mismatch"
	CodeCumulativeQuotaRegressed = "cumulative_quota_regressed"
	CodePayloadTooLarge          = console.CodePayloadTooLarge
	CodeInternalError            = console.CodeInternalError

	// codeInjected is the fake's own, for responses a Fault forces. A forced
	// 500 says internal_error instead, as the real console does.
	codeInjected = "injected_failure"
)

// DefaultRate and DefaultBurst are the console's dedicated bridge limiter.
// SetRateLimit(DefaultRate, DefaultBurst) turns it on; it is off by default so
// unrelated tests never see a 429.
const (
	DefaultRate  = 50
	DefaultBurst = 100
)

// MaxExtendIDLen bounds extend_id, which the client generates as ext_+ULID.
const MaxExtendIDLen = 64

type HoldStatus string

const (
	HoldActive   HoldStatus = "active"
	HoldSettled  HoldStatus = "settled"
	HoldReleased HoldStatus = "released"
	HoldExpired  HoldStatus = "expired"
)

// Tenant is a billing entity. Users with TenantID > 0 spend its balance.
type Tenant struct {
	ID        int
	Quota     int64
	Suspended bool
}

// User is a billing entity when TenantID == 0; otherwise Quota is ignored.
type User struct {
	ID       int
	TenantID int
	Group    string
	Disabled bool
	Quota    int64
}

// Key is a customer API key (a console token).
type Key struct {
	Key         string
	TokenID     int
	UserID      int
	Disabled    bool
	ExpiresAt   int64 // Unix seconds; 0 never expires
	Unlimited   bool
	RemainQuota int64
}

type Hold struct {
	HoldID    string
	UserID    int
	TenantID  int
	TokenID   int
	JobID     string
	SKU       string
	Quota     int64
	Settled   int64
	Released  int64
	Status    HoldStatus
	ExpiresAt int64
}

// ConsumeLog mirrors the row the spec says RecordConsumeLog writes per settle.
type ConsumeLog struct {
	UserID           int
	TenantID         int
	TokenID          int
	ModelName        string
	PromptTokens     int
	CompletionTokens int
	Quota            int64
	GPUSeconds       int64
	JobID            string
	HoldID           string
	EventID          string
}

// Call is one request the fake received, whatever happened to it.
type Call struct {
	Route    string // one of the Route* constants, or "" for an unknown path
	Method   string
	Path     string
	Body     []byte
	Status   int  // status written; 0 when the connection was dropped
	Injected bool // a Fault or SetDown decided the outcome
	Applied  bool // the ledger was changed (or a replay was answered) by this call
	At       time.Time
}

// Fault makes the next Times calls to Route (any route when empty) fail.
// Status 0 drops the connection without a response, which is what a client
// sees when the console is unreachable or dies mid-request. AfterApply commits
// the operation first and then fails the response: the case where compute
// cannot know whether the console charged.
type Fault struct {
	Route      string
	Times      int
	Status     int
	AfterApply bool
}

// Server is the fake console. Seed it, point a console.Client at URL, and
// inspect the ledger afterwards. All methods are safe for concurrent use.
type Server struct {
	*httptest.Server

	secret []byte

	mu       sync.Mutex
	now      func() time.Time
	down     bool
	latency  time.Duration
	faults   []Fault
	calls    []Call
	tenants  map[int]*Tenant
	users    map[int]*User
	keys     map[string]*Key
	tokens   map[int]*Key
	holds    map[string]*Hold
	holdResp map[string]holdRecord
	events   map[string]settledEvent
	extends  map[string]extendRecord

	rate      float64
	burst     float64
	tokensNow float64
	lastFill  time.Time
	logs      []ConsumeLog
}

type holdRecord struct {
	req  holdRequest
	resp holdData
}

type extendRecord struct {
	holdID string
	amount int64
}

type settledEvent struct {
	holdID string
	resp   settleData
}

// New starts a fake console. A nil or empty secret leaves the bridge
// disabled, so every route answers 404, as the spec requires when
// COMPUTE_BRIDGE_SECRET is unset. A non-empty secret shorter than 32 bytes is
// a test bug and panics.
func New(secret []byte) *Server {
	if len(secret) > 0 && len(secret) < 32 {
		panic("consoletest: bridge secret must be at least 32 bytes")
	}
	s := &Server{
		secret:   append([]byte(nil), secret...),
		now:      time.Now,
		tenants:  map[int]*Tenant{},
		users:    map[int]*User{},
		keys:     map[string]*Key{},
		tokens:   map[int]*Key{},
		holds:    map[string]*Hold{},
		holdResp: map[string]holdRecord{},
		events:   map[string]settledEvent{},
		extends:  map[string]extendRecord{},
	}
	s.Server = httptest.NewServer(s.handler())
	return s
}

// Client returns a console.Client aimed at this fake, sharing its clock.
func (s *Server) Client() *console.Client {
	c, err := console.New(s.URL, s.secret)
	if err != nil {
		panic("consoletest: " + err.Error())
	}
	c.Now = s.Now
	return c
}

// Now is the fake's clock, used for the replay window and hold expiry.
func (s *Server) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

func (s *Server) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// --- seeding -------------------------------------------------------------

func (s *Server) SeedTenant(t Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.ID] = &t
}

func (s *Server) SeedUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.Group == "" {
		u.Group = "default"
	}
	s.users[u.ID] = &u
}

func (s *Server) SeedKey(k Key) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[k.Key] = &k
	s.tokens[k.TokenID] = &k
}

// --- failure injection ---------------------------------------------------

// SetDown makes every request fail at the transport level until cleared.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// SetLatency delays every response by d (abandoned if the client gives up).
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latency = d
}

func (s *Server) InjectFault(f Fault) {
	if f.Times <= 0 {
		f.Times = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, f)
}

// RefuseNext fails the next n calls to any route with status.
func (s *Server) RefuseNext(n, status int) {
	s.InjectFault(Fault{Times: n, Status: status})
}

// SetRateLimit enables the bridge limiter at rate requests per second with
// the given burst, measured on the fake's clock (so a frozen clock never
// refills). rate <= 0 turns it off. The bucket starts full.
func (s *Server) SetRateLimit(rate float64, burst int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rate, s.burst = rate, float64(burst)
	s.tokensNow, s.lastFill = float64(burst), s.now()
}

// allow takes one token, or reports how long until one is available.
func (s *Server) allow() (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rate <= 0 {
		return true, 0
	}
	now := s.now()
	if el := now.Sub(s.lastFill).Seconds(); el > 0 {
		s.tokensNow = min(s.burst, s.tokensNow+el*s.rate)
	}
	s.lastFill = now
	if s.tokensNow >= 1 {
		s.tokensNow--
		return true, 0
	}
	return false, time.Duration((1 - s.tokensNow) / s.rate * float64(time.Second))
}

func (s *Server) ClearFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
	s.down = false
	s.latency = 0
}

// --- inspection ----------------------------------------------------------

func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

func (s *Server) CallsTo(route string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Route == route {
			out = append(out, c)
		}
	}
	return out
}

// Balance is the billing entity's balance for userID: the tenant's when the
// user belongs to one, else the user's own.
func (s *Server) Balance(userID int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := s.entityBalance(userID)
	if b == nil {
		return 0
	}
	return *b
}

func (s *Server) TokenRemainQuota(tokenID int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.tokens[tokenID]; k != nil {
		return k.RemainQuota
	}
	return 0
}

func (s *Server) Hold(holdID string) (Hold, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holds[holdID]
	if !ok {
		return Hold{}, false
	}
	return *h, true
}

func (s *Server) ConsumeLogs() []ConsumeLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ConsumeLog(nil), s.logs...)
}

// Sweep expires every active hold past its expires_at, as the console's
// sweeper would. Each request also sweeps first, so tests only need this to
// observe expiry without making a call.
func (s *Server) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
}

// --- signature -----------------------------------------------------------

var (
	ErrBadTimestamp = errors.New("timestamp outside the replay window")
	ErrBadSignature = errors.New("signature mismatch")
)

// VerifySignature checks the bridge's two headers against the spec's
// canonical string, METHOD\nPATH\nTIMESTAMP\nEXACT_BODY, independently of
// console.Sign so the client is checked against the contract and not itself.
func VerifySignature(secret []byte, method, path, timestamp, signature string, body []byte, now time.Time) error {
	if timestamp == "" {
		return ErrBadTimestamp
	}
	if signature == "" {
		return ErrBadSignature
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrBadTimestamp
	}
	skew := now.Sub(time.Unix(ts, 0))
	if skew > MaxSkew || skew < -MaxSkew {
		return ErrBadTimestamp
	}
	got, err := hex.DecodeString(signature)
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s\n%s\n%s\n", method, path, timestamp)
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// --- HTTP ----------------------------------------------------------------

type response struct {
	status int
	msg    string
	data   any
}

func ok(data any) response                 { return response{status: http.StatusOK, data: data} }
func fail(status int, msg string) response { return response{status: status, msg: msg} }

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/compute/v1/verify", s.route(RouteVerify, s.verify))
	mux.HandleFunc("POST /api/compute/v1/holds", s.route(RouteHold, s.hold))
	mux.HandleFunc("POST /api/compute/v1/holds/{hold_id}/settle", s.route(RouteSettle, s.settle))
	mux.HandleFunc("POST /api/compute/v1/holds/{hold_id}/extend", s.route(RouteExtend, s.extend))
	mux.HandleFunc("POST /api/compute/v1/holds/{hold_id}/release", s.route(RouteRelease, s.release))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.secret) == 0 {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			s.record(Call{Method: r.Method, Path: r.URL.Path, Body: body, Status: http.StatusNotFound})
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) record(c Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.At = s.now()
	s.calls = append(s.calls, c)
}

// takeFault consumes the first fault matching route and phase.
func (s *Server) takeFault(route string, afterApply bool) (Fault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.faults {
		f := &s.faults[i]
		if f.AfterApply != afterApply || (f.Route != "" && f.Route != route) {
			continue
		}
		hit := *f
		f.Times--
		if f.Times == 0 {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
		}
		return hit, true
	}
	return Fault{}, false
}

func (s *Server) route(name string, apply func(r *http.Request, body []byte) response) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		call := Call{Route: name, Method: r.Method, Path: r.URL.Path}
		body, err := io.ReadAll(io.LimitReader(r.Body, console.MaxRequestBody+1))
		call.Body = body
		if err != nil {
			call.Status = http.StatusBadRequest
			s.record(call)
			writeEnvelope(w, fail(http.StatusBadRequest, CodeInvalidRequest))
			return
		}

		s.mu.Lock()
		down, latency := s.down, s.latency
		s.mu.Unlock()
		if down {
			call.Injected = true
			s.record(call)
			drop(w)
			return
		}
		if latency > 0 {
			select {
			case <-time.After(latency):
			case <-r.Context().Done():
				call.Injected = true
				s.record(call)
				return
			}
		}
		if f, hit := s.takeFault(name, false); hit {
			call.Injected = true
			call.Status = f.Status
			s.record(call)
			injected(w, f)
			return
		}

		if allowed, wait := s.allow(); !allowed {
			call.Status = http.StatusTooManyRequests
			s.record(call)
			secs := int64(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
			writeEnvelope(w, fail(http.StatusTooManyRequests, CodeRateLimited))
			return
		}

		// Size before signature, as the console orders it (spec revision
		// 2026-10-02), so an oversized body is never reported as a bad secret.
		if len(body) > console.MaxRequestBody {
			call.Status = http.StatusRequestEntityTooLarge
			s.record(call)
			writeEnvelope(w, fail(http.StatusRequestEntityTooLarge, CodePayloadTooLarge))
			return
		}

		if err := VerifySignature(s.secret, r.Method, r.URL.Path, r.Header.Get(console.HeaderTimestamp), r.Header.Get(console.HeaderSignature), body, s.Now()); err != nil {
			msg := CodeInvalidSignature
			if errors.Is(err, ErrBadTimestamp) {
				msg = CodeInvalidTimestamp
			}
			call.Status = http.StatusUnauthorized
			s.record(call)
			writeEnvelope(w, fail(http.StatusUnauthorized, msg))
			return
		}

		resp := apply(r, body)
		call.Applied = resp.status/100 == 2
		if f, hit := s.takeFault(name, true); hit {
			call.Injected = true
			call.Status = f.Status
			s.record(call)
			injected(w, f)
			return
		}
		call.Status = resp.status
		s.record(call)
		writeEnvelope(w, resp)
	}
}

func injected(w http.ResponseWriter, f Fault) {
	if f.Status == 0 {
		drop(w)
		return
	}
	if f.Status == http.StatusInternalServerError {
		writeEnvelope(w, fail(f.Status, CodeInternalError))
		return
	}
	writeEnvelope(w, fail(f.Status, codeInjected))
}

func drop(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

func writeEnvelope(w http.ResponseWriter, r response) {
	var buf bytes.Buffer
	success := r.status/100 == 2
	code := ""
	if !success {
		code = r.msg
	}
	_ = json.NewEncoder(&buf).Encode(struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
		Data    any    `json:"data"`
	}{success, r.msg, code, r.data})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, _ = w.Write(buf.Bytes())
}

func decode(body []byte, v any) bool {
	return json.Unmarshal(body, v) == nil
}
