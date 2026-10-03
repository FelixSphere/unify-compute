package console

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Tests for the 2026-10-02 revision of docs/spec/console-billing-bridge.md.

// verifyKeyOfBodyLen returns a key whose Verify body {"key":"…"} is n bytes.
func verifyKeyOfBodyLen(n int) string {
	return strings.Repeat("k", n-len(`{"key":""}`))
}

func TestClientDoesNotSendABodyOver32KiB(t *testing.T) {
	var calls atomic.Int32
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{}}`))
	})
	_, err := c.Verify(context.Background(), verifyKeyOfBodyLen(MaxRequestBody+1))
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("want ErrRequestTooLarge, got %v", err)
	}
	var e *Error
	if errors.As(err, &e) {
		t.Fatalf("a local refusal must not look like a console answer: %#v", e)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("oversized request reached the console %d times", n)
	}
}

func TestClientSendsABodyOfExactly32KiB(t *testing.T) {
	var got int
	c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = len(body)
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"status":"active"}}`))
	})
	if _, err := c.Verify(context.Background(), verifyKeyOfBodyLen(MaxRequestBody)); err != nil {
		t.Fatal(err)
	}
	if got != 32768 {
		t.Fatalf("console received %d bytes, want 32768", got)
	}
}

func TestClientSurfacesTheRevisionErrorCodes(t *testing.T) {
	for _, tt := range []struct {
		status int
		code   string
	}{
		{http.StatusInternalServerError, "internal_error"},
		{http.StatusRequestEntityTooLarge, "payload_too_large"},
	} {
		t.Run(tt.code, func(t *testing.T) {
			c := revisionClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"success":false,"message":"` + tt.code + `","code":"` + tt.code + `"}`))
			})
			_, err := c.Verify(context.Background(), "sk-test")
			var e *Error
			if !errors.As(err, &e) || e.Status != tt.status || e.Message != tt.code {
				t.Fatalf("got %#v", err)
			}
		})
	}
	if CodeInternalError != "internal_error" || CodePayloadTooLarge != "payload_too_large" {
		t.Fatalf("code constants drifted from the spec: %q %q", CodeInternalError, CodePayloadTooLarge)
	}
}
