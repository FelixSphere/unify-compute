package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion pins the shape of every Stripe request and response. The
// webhook endpoint must be created with the same version, so events arrive
// in the shape the handler parses (docs/staging.md, "Stripe").
const APIVersion = "2024-06-20"

const defaultBaseURL = "https://api.stripe.com"

// Stripe is the handful of Stripe API calls top-ups make, as form-encoded
// requests over net/http (ADR-0011 §4: no SDK is needed for five calls).
// Written from Stripe's public API reference only.
type Stripe struct {
	// Key is a restricted key (rk_test_… / rk_live_…) or a secret key.
	Key string
	// BaseURL is https://api.stripe.com unless a test points it at a fake.
	BaseURL string
	HTTP    *http.Client
}

// StripeError is Stripe's error object, or a transport failure with Status 0.
type StripeError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e *StripeError) Error() string {
	if e.Status == 0 {
		return "stripe: " + e.Message
	}
	return fmt.Sprintf("stripe: %d %s %s: %s", e.Status, e.Type, e.Code, e.Message)
}

// CheckoutSession is the part of Stripe's Checkout Session object top-ups
// read, from an API response or from an event's data.object.
type CheckoutSession struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	URL               string            `json:"url"`
	Status            string            `json:"status"`         // open, complete, expired
	PaymentStatus     string            `json:"payment_status"` // paid, unpaid, no_payment_required
	Mode              string            `json:"mode"`
	Currency          string            `json:"currency"`
	AmountTotal       int64             `json:"amount_total"`
	ClientReferenceID string            `json:"client_reference_id"`
	Customer          string            `json:"customer"`
	PaymentIntent     string            `json:"payment_intent"`
	ExpiresAt         int64             `json:"expires_at"`
	Livemode          bool              `json:"livemode"`
	Metadata          map[string]string `json:"metadata"`
}

type customer struct {
	ID string `json:"id"`
}

type paymentIntent struct {
	ID           string `json:"id"`
	LatestCharge *struct {
		ID         string `json:"id"`
		ReceiptURL string `json:"receipt_url"`
	} `json:"latest_charge"`
}

func (s *Stripe) do(ctx context.Context, method, path string, form url.Values, idempotencyKey string, out any) error {
	base := s.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	var body io.Reader
	target := strings.TrimRight(base, "/") + path
	if method == http.MethodGet {
		if len(form) > 0 {
			target += "?" + form.Encode()
		}
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("Stripe-Version", APIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return &StripeError{Message: err.Error()}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return &StripeError{Message: err.Error()}
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &StripeError{Status: res.StatusCode, Type: e.Error.Type, Code: e.Error.Code, Message: e.Error.Message}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &StripeError{Status: res.StatusCode, Message: "unreadable response: " + err.Error()}
	}
	return nil
}

// CreateCustomer is POST /v1/customers: one Customer per account (ADR-0011
// §4, fraud defences), tagged so it is never mistaken for another product's.
func (s *Stripe) CreateCustomer(ctx context.Context, email string, metadata map[string]string, idempotencyKey string) (string, error) {
	form := url.Values{"email": {email}, "description": {"Unify Compute account " + metadata["account_id"]}}
	putMetadata(form, "metadata", metadata)
	var c customer
	if err := s.do(ctx, http.MethodPost, "/v1/customers", form, idempotencyKey, &c); err != nil {
		return "", err
	}
	return c.ID, nil
}

// CreateCheckoutSession is POST /v1/checkout/sessions.
func (s *Stripe) CreateCheckoutSession(ctx context.Context, form url.Values, idempotencyKey string) (CheckoutSession, error) {
	var cs CheckoutSession
	err := s.do(ctx, http.MethodPost, "/v1/checkout/sessions", form, idempotencyKey, &cs)
	return cs, err
}

// ExpireCheckoutSession is POST /v1/checkout/sessions/{id}/expire. Stripe
// refuses it for a session that is no longer open.
func (s *Stripe) ExpireCheckoutSession(ctx context.Context, id string) (CheckoutSession, error) {
	var cs CheckoutSession
	err := s.do(ctx, http.MethodPost, "/v1/checkout/sessions/"+url.PathEscape(id)+"/expire", nil, "", &cs)
	return cs, err
}

// GetCheckoutSession is GET /v1/checkout/sessions/{id}.
func (s *Stripe) GetCheckoutSession(ctx context.Context, id string) (CheckoutSession, error) {
	var cs CheckoutSession
	err := s.do(ctx, http.MethodGet, "/v1/checkout/sessions/"+url.PathEscape(id), nil, "", &cs)
	return cs, err
}

// Receipt is GET /v1/payment_intents/{id} with latest_charge expanded: the
// charge id and its hosted receipt_url, which a Checkout Session does not
// carry.
func (s *Stripe) Receipt(ctx context.Context, paymentIntentID string) (chargeID, receiptURL string, err error) {
	var pi paymentIntent
	err = s.do(ctx, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(paymentIntentID),
		url.Values{"expand[]": {"latest_charge"}}, "", &pi)
	if err != nil || pi.LatestCharge == nil {
		return "", "", err
	}
	return pi.LatestCharge.ID, pi.LatestCharge.ReceiptURL, nil
}

func putMetadata(form url.Values, prefix string, metadata map[string]string) {
	for k, v := range metadata {
		form.Set(prefix+"["+k+"]", v)
	}
}
