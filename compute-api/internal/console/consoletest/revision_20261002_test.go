package consoletest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
)

// Tests for the 2026-10-02 revision of docs/spec/console-billing-bridge.md.

func rawPost(t *testing.T, s *Server, path string, body []byte, ts int64, signature string) rawResponse {
	t.Helper()
	req, _ := http.NewRequest("POST", s.URL+path, bytes.NewReader(body))
	req.Header.Set(console.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(console.HeaderSignature, signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := rawResponse{status: resp.StatusCode, header: resp.Header}
	out.body, _ = io.ReadAll(resp.Body)
	_ = json.Unmarshal(out.body, &out.env)
	return out
}

func TestFakeRefusesABodyOver32KiBAs413BeforeTheSignature(t *testing.T) {
	s, _ := seeded(t)
	path := "/api/compute/v1/verify"
	ts := s.Now().Unix()
	tooBig := []byte(`{"key":"` + strings.Repeat("a", console.MaxRequestBody) + `"}`)

	for name, sig := range map[string]string{
		"signed":   console.Sign(vectorSecret, "POST", path, ts, tooBig),
		"unsigned": "",
		"wrong":    strings.Repeat("0", 64),
	} {
		t.Run(name, func(t *testing.T) {
			r := rawPost(t, s, path, tooBig, ts, sig)
			if r.status != http.StatusRequestEntityTooLarge || r.env.Message != CodePayloadTooLarge || r.env.Success {
				t.Fatalf("want 413 %s, got %d %s", CodePayloadTooLarge, r.status, r.body)
			}
		})
	}
	for _, c := range s.CallsTo(RouteVerify) {
		if c.Status != http.StatusRequestEntityTooLarge || c.Applied {
			t.Fatalf("oversized call recorded as %+v", c)
		}
	}
}

func TestFakeAcceptsABodyOfExactly32KiB(t *testing.T) {
	s, _ := seeded(t)
	path := "/api/compute/v1/verify"
	ts := s.Now().Unix()
	body := []byte(`{"key":"` + strings.Repeat("a", console.MaxRequestBody-len(`{"key":""}`)) + `"}`)
	if len(body) != console.MaxRequestBody {
		t.Fatalf("body is %d bytes", len(body))
	}
	r := rawPost(t, s, path, body, ts, console.Sign(vectorSecret, "POST", path, ts, body))
	if r.status != http.StatusUnauthorized || r.env.Message != CodeInvalidKey {
		t.Fatalf("want the key checked (401 invalid_key), got %d %s", r.status, r.body)
	}
}

func TestFakeForcedServerErrorIsInternalError(t *testing.T) {
	s, c := seeded(t)
	s.InjectFault(Fault{Route: RouteVerify, Status: http.StatusInternalServerError})
	_, err := c.Verify(context.Background(), "sk-test")
	refusal(t, err, http.StatusInternalServerError, CodeInternalError)
}
