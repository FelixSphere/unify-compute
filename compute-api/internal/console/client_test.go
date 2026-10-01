package console

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The console's test suite must reproduce this exact vector. Change it on both
// sides at once or not at all.
func TestSignatureVector(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	body := []byte(`{"key":"sk-test"}`)
	got := Sign(secret, "POST", "/api/compute/v1/verify", 1757520000, body)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("POST\n/api/compute/v1/verify\n1757520000\n" + string(body)))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature mismatch\n got %s\nwant %s", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("unexpected signature length %d", len(got))
	}
}

func TestClientSignsAndUnwrapsEnvelope(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/compute/v1/verify" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get(HeaderTimestamp) != "1757520000" {
			t.Errorf("timestamp header %q", r.Header.Get(HeaderTimestamp))
		}
		want := Sign(secret, "POST", r.URL.Path, 1757520000, body)
		if r.Header.Get(HeaderSignature) != want {
			t.Errorf("bad signature")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"user_id":1042,"tenant_id":0,"token_id":771,"group":"default","status":"active","remain_quota":12500000}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Unix(1757520000, 0) }
	id, err := c.Verify(context.Background(), "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if id.UserID != 1042 || id.RemainQuota != 12500000 {
		t.Fatalf("unexpected identity %+v", id)
	}
}

func TestClientSurfacesConsoleRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"success":false,"message":"insufficient quota"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, []byte("0123456789abcdef0123456789abcdef"))
	_, err := c.Hold(context.Background(), HoldRequest{HoldID: "h", Quota: 1})
	var e *Error
	if !errors.As(err, &e) || e.Status != 402 || e.Message != "insufficient quota" {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsShortSecret(t *testing.T) {
	if _, err := New("http://x", []byte("short")); err == nil {
		t.Fatal("expected error")
	}
}
