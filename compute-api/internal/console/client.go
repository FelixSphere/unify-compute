// Package console is the compute side of the console billing bridge
// (docs/spec/console-billing-bridge.md). It talks to the UnifyAPI console over
// HTTP only; see ADR-0002 for why nothing else is acceptable.
package console

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	HeaderTimestamp = "X-Compute-Timestamp"
	HeaderSignature = "X-Compute-Signature"
	basePath        = "/api/compute/v1"
)

// Canonical returns the string both sides sign: METHOD, PATH, TIMESTAMP and
// the exact body, newline-separated. The console's test suite must pin the
// same vector as sign_test.go.
func Canonical(method, path string, ts int64, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(path)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(ts, 10))
	b.WriteByte('\n')
	b.Write(body)
	return b.Bytes()
}

func Sign(secret []byte, method, path string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(Canonical(method, path, ts, body))
	return hex.EncodeToString(mac.Sum(nil))
}

type Client struct {
	BaseURL string
	Secret  []byte
	HTTP    *http.Client
	Now     func() time.Time
}

func New(baseURL string, secret []byte) (*Client, error) {
	if len(secret) < 32 {
		return nil, errors.New("console bridge secret must be at least 32 bytes")
	}
	return &Client{
		BaseURL: baseURL,
		Secret:  secret,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		Now:     time.Now,
	}, nil
}

// Error is a non-2xx bridge response. Status carries the console's HTTP code
// so callers can map 402 → insufficient_balance, 409 → conflict, and so on.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("console bridge: %d %s", e.Status, e.Message) }

type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	fullPath := basePath + path
	ts := c.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+fullPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(c.Secret, http.MethodPost, fullPath, ts, body))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &Error{Status: resp.StatusCode, Message: "non-JSON response"}
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		return &Error{Status: resp.StatusCode, Message: env.Message}
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

type Identity struct {
	UserID           int    `json:"user_id"`
	TenantID         int    `json:"tenant_id"`
	TokenID          int    `json:"token_id"`
	Group            string `json:"group"`
	Status           string `json:"status"`
	RemainQuota      int64  `json:"remain_quota"`
	TokenUnlimited   bool   `json:"token_unlimited"`
	TokenRemainQuota int64  `json:"token_remain_quota"`
}

func (c *Client) Verify(ctx context.Context, key string) (*Identity, error) {
	var id Identity
	if err := c.post(ctx, "/verify", map[string]string{"key": key}, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

type HoldRequest struct {
	HoldID    string `json:"hold_id"`
	UserID    int    `json:"user_id"`
	TokenID   int    `json:"token_id"`
	Quota     int64  `json:"quota"`
	JobID     string `json:"job_id"`
	SKU       string `json:"sku"`
	ExpiresAt int64  `json:"expires_at"`
}

type HoldResult struct {
	HoldID      string `json:"hold_id"`
	Quota       int64  `json:"quota"`
	RemainQuota int64  `json:"remain_quota"`
}

func (c *Client) Hold(ctx context.Context, req HoldRequest) (*HoldResult, error) {
	var out HoldResult
	if err := c.post(ctx, "/holds", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type SettleRequest struct {
	EventID         string `json:"event_id"`
	CumulativeQuota int64  `json:"cumulative_quota"`
	GPUSeconds      int64  `json:"gpu_seconds"`
	SKU             string `json:"sku"`
	Final           bool   `json:"final"`
}

type SettleResult struct {
	HoldID        string `json:"hold_id"`
	SettledQuota  int64  `json:"settled_quota"`
	HoldRemaining int64  `json:"hold_remaining"`
}

func (c *Client) Settle(ctx context.Context, holdID string, req SettleRequest) (*SettleResult, error) {
	var out SettleResult
	if err := c.post(ctx, "/holds/"+holdID+"/settle", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Extend(ctx context.Context, holdID string, additionalQuota int64) (*HoldResult, error) {
	var out HoldResult
	in := map[string]int64{"additional_quota": additionalQuota}
	if err := c.post(ctx, "/holds/"+holdID+"/extend", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type ReleaseResult struct {
	ReleasedQuota int64 `json:"released_quota"`
	RemainQuota   int64 `json:"remain_quota"`
}

func (c *Client) Release(ctx context.Context, holdID string) (*ReleaseResult, error) {
	var out ReleaseResult
	if err := c.post(ctx, "/holds/"+holdID+"/release", struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
