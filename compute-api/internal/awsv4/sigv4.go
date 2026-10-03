// Package awsv4 is the small part of AWS that compute-api talks to —
// Signature Version 4, instance-role credentials, STS AssumeRole and five S3
// object calls — written against the standard library.
//
// Why not the AWS SDK for Go v2: the artifact store needs SigV4 signing and
// presigning, one STS call and a handful of S3 calls. The SDK would bring
// aws-sdk-go-v2, config, credentials, feature/ec2/imds, service/sts,
// service/s3 and smithy-go (and their internal modules) for that. SigV4 is a
// published algorithm with published test vectors, which sigv4_test.go
// checks byte for byte, so the stdlib version is both smaller and
// verifiable. If compute-api ever needs more of AWS than this, switch to the
// SDK rather than growing this package.
package awsv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm   = "AWS4-HMAC-SHA256"
	amzDate     = "20060102T150405Z"
	shortDate   = "20060102"
	unsignedPay = "UNSIGNED-PAYLOAD"
	// EmptySHA256 is the payload hash of an empty body.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Credentials are one AWS access key, with the session token and expiry of
// temporary credentials. A zero Expires means they do not expire.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expires         time.Time
}

// Signer signs requests for one service in one region.
type Signer struct {
	Region  string
	Service string
}

func HashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Sign adds SigV4 Authorization to req. payloadHash is the hex SHA-256 of
// the body (EmptySHA256 for none). Every header already on req is signed,
// plus host and x-amz-date, so set the headers that matter before calling.
func (s Signer) Sign(req *http.Request, c Credentials, payloadHash string, now time.Time) {
	now = now.UTC()
	req.Header.Set("X-Amz-Date", now.Format(amzDate))
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	if s.Service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}
	headers, signed := canonicalHeaders(req)
	creq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.Query()),
		headers,
		signed,
		payloadHash,
	}, "\n")
	scope := s.scope(now)
	sig := s.signature(c.SecretAccessKey, now, stringToSign(now, scope, creq))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, c.AccessKeyID, scope, signed, sig))
}

// Presign returns u with a query-string signature valid for expires.
// headers are extra headers the client must send exactly as given; they are
// signed, so S3 refuses the request without them.
func (s Signer) Presign(method string, u *url.URL, c Credentials, headers http.Header, expires time.Duration, now time.Time) string {
	now = now.UTC()
	q := u.Query()
	scope := s.scope(now)
	req := &http.Request{Method: method, URL: u, Host: u.Host, Header: http.Header{}}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	hdrs, signed := canonicalHeaders(req)
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", c.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", now.Format(amzDate))
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires/time.Second)))
	q.Set("X-Amz-SignedHeaders", signed)
	if c.SessionToken != "" {
		q.Set("X-Amz-Security-Token", c.SessionToken)
	}
	creq := strings.Join([]string{method, canonicalURI(u), canonicalQuery(q), hdrs, signed, unsignedPay}, "\n")
	q.Set("X-Amz-Signature", s.signature(c.SecretAccessKey, now, stringToSign(now, scope, creq)))
	out := *u
	out.RawQuery = canonicalQuery(q)
	return out.String()
}

func (s Signer) scope(now time.Time) string {
	return now.Format(shortDate) + "/" + s.Region + "/" + s.Service + "/aws4_request"
}

func stringToSign(now time.Time, scope, creq string) string {
	return algorithm + "\n" + now.Format(amzDate) + "\n" + scope + "\n" + HashHex([]byte(creq))
}

func (s Signer) signature(secret string, now time.Time, sts string) string {
	k := hmacSHA256([]byte("AWS4"+secret), now.Format(shortDate))
	k = hmacSHA256(k, s.Region)
	k = hmacSHA256(k, s.Service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, sts))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func canonicalHeaders(req *http.Request) (string, string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	vals := map[string]string{"host": host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" || lk == "user-agent" {
			continue
		}
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		vals[lk] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + ":" + vals[n] + "\n")
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalURI encodes each path segment once, as S3 expects (other
// services double-encode, but every non-S3 call here has path "/").
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	raw, err := url.PathUnescape(p)
	if err != nil {
		raw = u.Path
	}
	segs := strings.Split(raw, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode is SigV4's UriEncode: every byte except A-Z a-z 0-9 - _ . ~ is
// percent-encoded with uppercase hex.
func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// EscapeKey percent-encodes an object key for a URL path, keeping "/".
func EscapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}
