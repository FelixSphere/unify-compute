// Package stripetest is a fake of the Stripe API endpoints internal/payments
// calls, for tests: customers, Checkout Sessions (create, expire, retrieve)
// and a PaymentIntent's latest charge. It keeps state, honours idempotency
// keys the way Stripe documents them (same key, same answer), records every
// request, and builds signed webhook deliveries. Shapes follow Stripe's
// public API reference for the version payments.APIVersion pins.
package stripetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/payments"
)

const (
	Key    = "rk_test_fakeComputeRestrictedKey"
	Secret = "whsec_fakeComputeEndpointSecret"
)

type Request struct {
	Method, Path   string
	Form           url.Values
	IdempotencyKey string
	Version        string
}

type Server struct {
	*httptest.Server
	// Now dates expires_at defaults; nil is time.Now.
	Now func() time.Time

	mu       sync.Mutex
	n        int
	sessions map[string]*payments.CheckoutSession
	idem     map[string][]byte
	requests []Request
	// failNext answers the next n requests 500 api_error.
	failNext int
}

func New(t *testing.T) *Server {
	s := &Server{sessions: map[string]*payments.CheckoutSession{}, idem: map[string][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Client is a payments.Stripe pointed at the fake.
func (s *Server) Client() *payments.Stripe {
	return &payments.Stripe{Key: Key, BaseURL: s.URL, HTTP: s.Server.Client()}
}

// FailNext makes the next n requests fail as Stripe's 500 api_error.
func (s *Server) FailNext(n int) {
	s.mu.Lock()
	s.failNext = n
	s.mu.Unlock()
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Calls lists "METHOD /path" of every request, in order.
func (s *Server) Calls() []string {
	var out []string
	for _, r := range s.Requests() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (s *Server) Session(id string) payments.CheckoutSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs := s.sessions[id]; cs != nil {
		return *cs
	}
	return payments.CheckoutSession{}
}

// Pay completes a session as a successful card payment would.
func (s *Server) Pay(id string) payments.CheckoutSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.sessions[id]
	cs.Status, cs.PaymentStatus = "complete", "paid"
	s.n++
	cs.PaymentIntent = fmt.Sprintf("pi_fake%d", s.n)
	return *cs
}

// Expire ends a session as Stripe does when expires_at passes.
func (s *Server) Expire(id string) payments.CheckoutSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.sessions[id]
	cs.Status = "expired"
	return *cs
}

// Event is a webhook event body around obj, as Stripe sends it.
func Event(id, typ string, livemode bool, obj any) []byte {
	b, err := json.Marshal(map[string]any{"id": id, "object": "event", "api_version": payments.APIVersion, "type": typ,
		"livemode": livemode, "created": 1791000000, "data": map[string]any{"object": obj}})
	if err != nil {
		panic(err)
	}
	return b
}

// SessionObject renders a session as it appears in an event's data.object.
func SessionObject(cs payments.CheckoutSession) map[string]any {
	md := map[string]string{}
	for k, v := range cs.Metadata {
		md[k] = v
	}
	var pi any
	if cs.PaymentIntent != "" {
		pi = cs.PaymentIntent
	}
	return map[string]any{"id": cs.ID, "object": "checkout.session", "url": nil, "status": cs.Status, "payment_status": cs.PaymentStatus,
		"mode": cs.Mode, "currency": cs.Currency, "amount_total": cs.AmountTotal, "amount_subtotal": cs.AmountTotal,
		"client_reference_id": cs.ClientReferenceID, "customer": cs.Customer, "payment_intent": pi,
		"expires_at": cs.ExpiresAt, "livemode": cs.Livemode, "metadata": md}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := r.PostForm
	if r.Method == http.MethodGet {
		form = r.URL.Query()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Form: form,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Version: r.Header.Get("Stripe-Version")})
	if r.Header.Get("Authorization") != "Bearer "+Key {
		stripeError(w, http.StatusUnauthorized, "invalid_request_error", "", "Invalid API Key provided.")
		return
	}
	if s.failNext > 0 {
		s.failNext--
		stripeError(w, http.StatusInternalServerError, "api_error", "", "Something went wrong on Stripe's end.")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if idem != "" {
		if b, ok := s.idem[r.URL.Path+" "+idem]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			_, _ = w.Write(b)
			return
		}
	}
	var out any
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/customers":
		s.n++
		out = map[string]any{"id": fmt.Sprintf("cus_fake%d", s.n), "object": "customer", "email": form.Get("email"), "metadata": metadata(form, "metadata")}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
		cs, msg := s.create(form)
		if msg != "" {
			stripeError(w, http.StatusBadRequest, "invalid_request_error", "parameter_invalid", msg)
			return
		}
		out = s.view(cs)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") && strings.HasSuffix(r.URL.Path, "/expire"):
		cs := s.sessions[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/"), "/expire")]
		if cs == nil {
			stripeError(w, http.StatusNotFound, "invalid_request_error", "resource_missing", "No such checkout.session")
			return
		}
		if cs.Status != "open" {
			stripeError(w, http.StatusBadRequest, "invalid_request_error", "", "Only Checkout Sessions with a status of open can be expired.")
			return
		}
		cs.Status = "expired"
		out = s.view(cs)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
		cs := s.sessions[strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")]
		if cs == nil {
			stripeError(w, http.StatusNotFound, "invalid_request_error", "resource_missing", "No such checkout.session")
			return
		}
		out = s.view(cs)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/")
		var charge any = "ch_for_" + id
		if form.Get("expand[]") == "latest_charge" {
			charge = map[string]any{"id": "ch_for_" + id, "object": "charge", "receipt_url": "https://pay.stripe.com/receipts/payment/" + id}
		}
		out = map[string]any{"id": id, "object": "payment_intent", "latest_charge": charge}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
		amount, err := strconv.ParseInt(form.Get("amount"), 10, 64)
		if form.Get("payment_intent") == "" || err != nil || amount <= 0 {
			stripeError(w, http.StatusBadRequest, "invalid_request_error", "parameter_invalid", "payment_intent and a positive amount are required.")
			return
		}
		s.n++
		out = map[string]any{"id": fmt.Sprintf("re_fake%d", s.n), "object": "refund", "amount": amount, "currency": "usd",
			"payment_intent": form.Get("payment_intent"), "status": "succeeded", "reason": form.Get("reason"), "metadata": metadata(form, "metadata")}
	default:
		stripeError(w, http.StatusNotFound, "invalid_request_error", "", "Unrecognized request URL ("+r.Method+": "+r.URL.Path+").")
		return
	}
	b, _ := json.Marshal(out)
	if idem != "" {
		s.idem[r.URL.Path+" "+idem] = b
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// create checks what Stripe would refuse, and what top-ups promise
// (one-off card payment in USD, nothing saved), and makes the session.
func (s *Server) create(f url.Values) (*payments.CheckoutSession, string) {
	need := []string{"mode", "line_items[0][price_data][currency]", "line_items[0][price_data][unit_amount]",
		"line_items[0][price_data][product_data][name]", "line_items[0][quantity]", "success_url", "cancel_url"}
	for _, k := range need {
		if f.Get(k) == "" {
			return nil, "Missing required param: " + k + "."
		}
	}
	amount, err := strconv.ParseInt(f.Get("line_items[0][price_data][unit_amount]"), 10, 64)
	qty, err2 := strconv.ParseInt(f.Get("line_items[0][quantity]"), 10, 64)
	if err != nil || err2 != nil || amount <= 0 || qty <= 0 {
		return nil, "Invalid integer."
	}
	if f.Get("payment_intent_data[setup_future_usage]") != "" {
		return nil, "setup_future_usage would save the card"
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	expires := now.Add(24 * time.Hour).Unix()
	if v := f.Get("expires_at"); v != "" {
		e, err := strconv.ParseInt(v, 10, 64)
		if err != nil || e < now.Add(30*time.Minute).Unix() || e > now.Add(24*time.Hour).Unix() {
			return nil, "The `expires_at` timestamp must be between 30 minutes and 24 hours from Checkout Session creation."
		}
		expires = e
	}
	s.n++
	id := fmt.Sprintf("cs_test_fake%d", s.n)
	cs := &payments.CheckoutSession{ID: id, Object: "checkout.session", URL: "https://checkout.stripe.com/c/pay/" + id, Status: "open",
		PaymentStatus: "unpaid", Mode: f.Get("mode"), Currency: f.Get("line_items[0][price_data][currency]"), AmountTotal: amount * qty,
		ClientReferenceID: f.Get("client_reference_id"), Customer: f.Get("customer"), ExpiresAt: expires, Metadata: metadata(f, "metadata")}
	s.sessions[id] = cs
	return cs, ""
}

func (s *Server) view(cs *payments.CheckoutSession) map[string]any {
	v := SessionObject(*cs)
	if cs.Status == "open" {
		v["url"] = cs.URL
	}
	return v
}

func metadata(f url.Values, prefix string) map[string]string {
	md := map[string]string{}
	for k, v := range f {
		if strings.HasPrefix(k, prefix+"[") && strings.HasSuffix(k, "]") && v[0] != "" {
			md[strings.TrimSuffix(strings.TrimPrefix(k, prefix+"["), "]")] = v[0]
		}
	}
	return md
}

func stripeError(w http.ResponseWriter, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": typ, "code": code, "message": msg}})
}
