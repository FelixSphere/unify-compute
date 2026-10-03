package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SignatureTolerance is how far a Stripe-Signature timestamp may be from our
// clock, either way (Stripe's documented default; spec/accounts.md "Stripe
// webhook").
const SignatureTolerance = 5 * time.Minute

var (
	ErrNoSignature    = errors.New("payments: Stripe-Signature missing or malformed")
	ErrBadSignature   = errors.New("payments: no v1 signature matches")
	ErrStaleSignature = errors.New("payments: Stripe-Signature timestamp outside the tolerance")
)

// VerifySignature checks a Stripe-Signature header against the raw request
// body, by Stripe's documented scheme: the header is comma-separated
// key=value pairs with one t (Unix seconds) and one or more v1 (hex
// HMAC-SHA256 of "<t>.<body>" under the endpoint's whsec_ secret; more than
// one while a secret is being rolled). Any matching v1 passes; v0 and
// unknown keys are ignored. The comparison is constant-time, and the
// timestamp is checked after the signature, so a replayed old delivery is
// refused even though its signature is genuine.
func VerifySignature(header string, body []byte, secret string, now time.Time) error {
	var ts int64 = -1
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || ts != -1 {
				return ErrNoSignature
			}
			ts = n
		case "v1":
			if b, err := hex.DecodeString(v); err == nil && len(b) == sha256.Size {
				sigs = append(sigs, b)
			}
		}
	}
	if ts < 0 || len(sigs) == 0 || secret == "" {
		return ErrNoSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	matched := false
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			matched = true
		}
	}
	if !matched {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > SignatureTolerance || d < -SignatureTolerance {
		return ErrStaleSignature
	}
	return nil
}

// Sign produces a Stripe-Signature header for body at t, as Stripe would.
// Tests and the local fake use it; production only verifies.
func Sign(body []byte, secret string, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
