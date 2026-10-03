package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// An upload token is what a job's run step presents to compute-api to get
// upload URLs for its own prefix. It is an HMAC over (owner, job, expiry)
// with a server secret: compute-api keeps no per-token state, any replica
// can check it, and it names the owner so the job can be looked up through
// the same owner-scoped store read the customer routes use.
//
// It is a bearer capability, visible to the customer's own code through
// the environment. What it can do is bounded three ways: only this job's
// prefix (the key is built server-side from the job id, and the session
// policy repeats that), only until it expires, and only while the job is
// not terminal.

const tokenVersion = "uct1"

var ErrBadToken = errors.New("artifacts: upload token is invalid or expired")

type claims struct {
	Owner string `json:"o"`
	Job   string `json:"j"`
	Exp   int64  `json:"e"`
}

func mintToken(secret []byte, owner, jobID string, exp time.Time) string {
	b, _ := json.Marshal(claims{Owner: owner, Job: jobID, Exp: exp.Unix()})
	body := tokenVersion + "." + base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(mac(secret, body))
}

func mac(secret []byte, body string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(body))
	return m.Sum(nil)
}

// verifyToken returns the owner the token was minted for, if it is
// authentic, unexpired and minted for jobID.
func verifyToken(secret []byte, jobID, tok string, now time.Time) (string, error) {
	i := strings.LastIndexByte(tok, '.')
	if i < 0 || !strings.HasPrefix(tok, tokenVersion+".") {
		return "", ErrBadToken
	}
	body, sig := tok[:i], tok[i+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(secret, body)) {
		return "", ErrBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(body, tokenVersion+"."))
	if err != nil {
		return "", ErrBadToken
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil || c.Job != jobID || c.Owner == "" || !now.Before(time.Unix(c.Exp, 0)) {
		return "", ErrBadToken
	}
	return c.Owner, nil
}
