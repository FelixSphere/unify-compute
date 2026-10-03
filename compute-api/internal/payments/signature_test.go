package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"
)

const secret = "whsec_test_signing_secret"

// stripeHeader builds Stripe-Signature independently of Sign, from Stripe's
// documented scheme, so the verifier is not only checked against itself.
func stripeHeader(t int64, body, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strconv.FormatInt(t, 10) + "." + body))
	return "t=" + strconv.FormatInt(t, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	now := time.Unix(1791000000, 0)
	body := `{"id":"evt_1","type":"checkout.session.completed"}`
	good := stripeHeader(now.Unix(), body, secret)
	if Sign([]byte(body), secret, now) != good {
		t.Fatalf("Sign disagrees with the documented scheme")
	}
	cases := []struct {
		name   string
		header string
		body   string
		at     time.Time
		want   error
	}{
		{"valid", good, body, now, nil},
		{"valid four minutes later", good, body, now.Add(4 * time.Minute), nil},
		{"valid with a v0 and a second, stale v1 while the secret rolls",
			"t=1791000000,v1=" + hex.EncodeToString(make([]byte, 32)) + ",v0=abc," + good[len("t=1791000000,"):], body, now, nil},
		{"wrong secret", stripeHeader(now.Unix(), body, "whsec_someone_else"), body, now, ErrBadSignature},
		{"tampered body", good, `{"id":"evt_1","type":"checkout.session.completed","x":1}`, now, ErrBadSignature},
		{"stale timestamp", good, body, now.Add(5*time.Minute + time.Second), ErrStaleSignature},
		{"timestamp from the future", good, body, now.Add(-5*time.Minute - time.Second), ErrStaleSignature},
		{"replayed old delivery", stripeHeader(now.Add(-time.Hour).Unix(), body, secret), body, now, ErrStaleSignature},
		{"timestamp swapped under the signature", "t=1791000001" + good[len("t=1791000000"):], body, now, ErrBadSignature},
		{"missing", "", body, now, ErrNoSignature},
		{"no v1", "t=1791000000,v0=" + good[len("t=1791000000,v1="):], body, now, ErrNoSignature},
		{"no timestamp", good[len("t=1791000000,"):], body, now, ErrNoSignature},
		{"two timestamps", "t=1,t=1791000000," + good[len("t=1791000000,"):], body, now, ErrNoSignature},
		{"garbage", "nonsense", body, now, ErrNoSignature},
	}
	for _, c := range cases {
		err := VerifySignature(c.header, []byte(c.body), secret, c.at)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if err := VerifySignature(good, []byte(body), "", now); !errors.Is(err, ErrNoSignature) {
		t.Errorf("empty secret verified: %v", err)
	}
}

func TestSettingsCheck(t *testing.T) {
	const live, test, rlive, rtest = "sk_live_abc123", "sk_test_abc123", "rk_live_abc123", "rk_test_abc123"
	cases := []struct {
		key, whsec, env string
		ok              bool
	}{
		{rtest, "whsec_x", "staging", true},
		{test, "whsec_x", "staging", true},
		{rtest, "whsec_x", "local", true},
		{rlive, "whsec_x", "production", true},
		{live, "whsec_x", "production", true},
		{rlive, "whsec_x", "staging", false},
		{live, "whsec_x", "staging", false},
		{live, "whsec_x", "", false},
		{rtest, "whsec_x", "production", false},
		{test, "whsec_x", "production", false},
		{rtest, "", "staging", false},
		{"", "whsec_x", "staging", false},
		{"pk_test_abc", "whsec_x", "staging", false},
		{rtest, "secret", "staging", false},
	}
	for _, c := range cases {
		s := Settings{SecretKey: c.key, WebhookSecret: c.whsec, Environment: c.env}
		if err := s.Check(); (err == nil) != c.ok {
			t.Errorf("%+v: Check() = %v, want ok=%v", s, err, c.ok)
		}
	}
	if (Settings{}).Configured() {
		t.Error("no keys reported as configured")
	}
	if !(Settings{WebhookSecret: "whsec_x"}).Configured() {
		t.Error("half the keys reported as unconfigured; Check would never see them")
	}
}

func TestParseAmount(t *testing.T) {
	good := map[string]int{"10": 1000, "10.00": 1000, "20.5": 2050, "99.99": 9999, "100": 10000, "0.01": 1, " 15 ": 1500}
	for in, want := range good {
		if got, ok := ParseAmount(in); !ok || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "-5", "10.001", "1e3", "010", "10.", ".5", "1,000", "10 USD", "99999999"} {
		if _, ok := ParseAmount(in); ok {
			t.Errorf("ParseAmount(%q) accepted", in)
		}
	}
	if CentsUSD(2050) != "20.50" || CentsUSD(1) != "0.01" {
		t.Errorf("CentsUSD: %s %s", CentsUSD(2050), CentsUSD(1))
	}
}
