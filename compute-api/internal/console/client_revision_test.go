package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Tests for the 2026-10-01 revision of docs/spec/console-billing-bridge.md.

const revisionSecret = "0123456789abcdef0123456789abcdef"

func revisionClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, []byte(revisionSecret))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExtendSendsExtendID(t *testing.T) {
	var got map[string]any
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/compute/v1/holds/hold_1/extend" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body %s: %v", body, err)
		}
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"hold_id":"hold_1","quota":1570000,"remain_quota":10930000}}`))
	})
	res, err := c.Extend(context.Background(), "hold_1", ExtendRequest{ExtendID: "ext_01J8ZZZZZZZZZZZZZZZZZZZZZZ", AdditionalQuota: 300000})
	if err != nil {
		t.Fatal(err)
	}
	if got["extend_id"] != "ext_01J8ZZZZZZZZZZZZZZZZZZZZZZ" || got["additional_quota"] != float64(300000) || len(got) != 2 {
		t.Fatalf("request body %v", got)
	}
	if res.Quota != 1570000 || res.RemainQuota != 10930000 {
		t.Fatalf("result %+v", res)
	}
}

func TestExtendIdempotencyConflictIs409(t *testing.T) {
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"message":"idempotency_conflict"}`))
	})
	_, err := c.Extend(context.Background(), "hold_1", ExtendRequest{ExtendID: "ext_x", AdditionalQuota: 1})
	var e *Error
	if !errors.As(err, &e) || e.Status != 409 || e.Message != CodeIdempotencyConflict {
		t.Fatalf("got %v", err)
	}
}

func TestVerify403SurfacesAccountStatus(t *testing.T) {
	for _, status := range []string{AccountSuspended, AccountDisabled} {
		t.Run(status, func(t *testing.T) {
			c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"success":false,"message":"user is banned","data":{"status":"` + status + `"}}`))
			})
			id, err := c.Verify(context.Background(), "sk-test")
			if id != nil {
				t.Fatalf("identity returned on 403: %+v", id)
			}
			var e *Error
			if !errors.As(err, &e) || e.Status != 403 || e.AccountStatus != status || e.Message != "user is banned" {
				t.Fatalf("got %#v", err)
			}
		})
	}
}

func TestBodiless404IsBridgeDisabled(t *testing.T) {
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.Verify(context.Background(), "sk-test")
	if !errors.Is(err, ErrBridgeDisabled) || errors.Is(err, ErrHoldNotFound) {
		t.Fatalf("got %v", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Status != 404 || e.Message != CodeBridgeDisabled {
		t.Fatalf("got %#v", err)
	}
}

func TestJSON404IsHoldNotFound(t *testing.T) {
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"message":"hold_not_found"}`))
	})
	_, err := c.Release(context.Background(), "hold_missing")
	if !errors.Is(err, ErrHoldNotFound) || errors.Is(err, ErrBridgeDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestNonJSON404IsNeitherBridgeDisabledNorHoldNotFound(t *testing.T) {
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := c.Verify(context.Background(), "sk-test")
	if err == nil || errors.Is(err, ErrBridgeDisabled) || errors.Is(err, ErrHoldNotFound) {
		t.Fatalf("got %v", err)
	}
}

// The value the console's bridge test pins for TestSignatureVector's inputs
// (unifyapi-console PR #225); client_test.go recomputes it instead.
func TestSignatureVectorLiteral(t *testing.T) {
	got := Sign([]byte(revisionSecret), "POST", "/api/compute/v1/verify", 1757520000, []byte(`{"key":"sk-test"}`))
	if want := "aba83132084d4c2cfefd1fd39b324e90358975c6b5ade128a7a6b960285eb29c"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
