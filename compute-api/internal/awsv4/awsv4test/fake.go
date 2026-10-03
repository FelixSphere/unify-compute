// Package awsv4test is an in-process fake of the STS, S3 and SES calls
// compute-api makes, for tests. It checks every request's SigV4 signature
// (header or presigned) against the secret it issued, and it enforces the
// rules the real account enforces through IAM:
//
//   - the instance key may read and list, and may not write (the instance
//     role in deploy/aws/modules/compute-stack has no s3:PutObject on the
//     artifacts bucket);
//   - a key issued by AssumeRole may only PutObject on the resources its
//     session policy names, and AssumeRole refuses more than one hour, as
//     AWS does for role chaining;
//   - only the instance key may call SES SendEmail (the instance role has
//     ses:SendEmail, deploy/aws/modules/compute-stack; job sessions do not).
//
// It is not an IAM evaluator: it reads the session policy's Resource
// patterns (trailing "*" only) and nothing else.
package awsv4test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
)

type Object struct {
	Body      []byte
	SHA256B64 string
}

type Assumed struct {
	RoleARN     string
	SessionName string
	Policy      string
	Duration    time.Duration
}

// Email is one SES v2 SendEmail request the fake accepted.
type Email struct {
	From             string
	To               []string
	ConfigurationSet string
	Raw              []byte
}

type Fake struct {
	Server   *httptest.Server
	Region   string
	Bucket   string
	Instance awsv4.Credentials
	Now      func() time.Time

	mu       sync.Mutex
	objects  map[string]Object
	sessions map[string]session // access key id → session
	Assumes  []Assumed
	Emails   []Email
	// SESError, when set, is the error type SES answers every SendEmail
	// with (e.g. "MessageRejected"), as a 400.
	SESError string
	n        int
}

type session struct {
	secret    string
	token     string
	resources []string
	expires   time.Time
}

func New(t *testing.T, bucket string) *Fake {
	f := &Fake{
		Region:   "us-east-1",
		Bucket:   bucket,
		Instance: awsv4.Credentials{AccessKeyID: "AKIDINSTANCE", SecretAccessKey: "instance-secret", SessionToken: "instance-token"},
		Now:      time.Now,
		objects:  map[string]Object{},
		sessions: map[string]session{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// Client is an awsv4.Client pointed at the fake.
func (f *Fake) Client() *awsv4.Client {
	return &awsv4.Client{Region: f.Region, S3Endpoint: f.Server.URL, STSEndpoint: f.Server.URL + "/sts/",
		SESEndpoint: f.Server.URL + "/ses", HTTP: f.Server.Client(), Now: f.Now}
}

func (f *Fake) Put(key string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := sha256.Sum256(body)
	f.objects[key] = Object{Body: body, SHA256B64: base64.StdEncoding.EncodeToString(h[:])}
}

func (f *Fake) Object(key string) (Object, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return o, ok
}

func (f *Fake) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ks []string
	for k := range f.objects {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func s3err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, msg)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key, err := f.authenticate(r, body)
	if err != nil {
		s3err(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	if strings.HasPrefix(r.URL.Path, "/sts/") {
		f.assumeRole(w, r, key, body)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/ses/") {
		f.sendEmail(w, r, key, body)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+f.Bucket+"/")
	if !ok {
		s3err(w, http.StatusNotFound, "NoSuchBucket", r.URL.Path)
		return
	}
	objKey := rest
	instance := key == f.Instance.AccessKeyID
	switch {
	case r.Method == http.MethodGet && objKey == "" && r.URL.Query().Get("list-type") == "2":
		if !instance {
			s3err(w, http.StatusForbidden, "AccessDenied", "list")
			return
		}
		f.list(w, r.URL.Query().Get("prefix"))
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		if !instance {
			s3err(w, http.StatusForbidden, "AccessDenied", "read")
			return
		}
		o, ok := f.Object(objKey)
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s3err(w, http.StatusNotFound, "NoSuchKey", objKey)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(o.Body)))
		if r.Header.Get("X-Amz-Checksum-Mode") == "ENABLED" && o.SHA256B64 != "" {
			w.Header().Set("X-Amz-Checksum-Sha256", o.SHA256B64)
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.Body)
		}
	case r.Method == http.MethodPut:
		if instance || !f.allowed(key, objKey) {
			s3err(w, http.StatusForbidden, "AccessDenied", "put "+objKey)
			return
		}
		h := sha256.Sum256(body)
		sum := base64.StdEncoding.EncodeToString(h[:])
		if want := r.Header.Get("X-Amz-Checksum-Sha256"); want != "" && want != sum {
			s3err(w, http.StatusBadRequest, "BadDigest", "checksum mismatch")
			return
		}
		f.mu.Lock()
		o := Object{Body: body}
		if r.Header.Get("X-Amz-Checksum-Sha256") != "" {
			o.SHA256B64 = sum
		}
		f.objects[objKey] = o
		f.mu.Unlock()
	default:
		s3err(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func (f *Fake) allowed(key, objKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[key]
	if !ok || !f.Now().Before(s.expires) {
		return false
	}
	arn := "arn:aws:s3:::" + f.Bucket + "/" + objKey
	for _, res := range s.resources {
		if p, wild := strings.CutSuffix(res, "*"); wild && strings.HasPrefix(arn, p) || res == arn {
			return true
		}
	}
	return false
}

func (f *Fake) list(w http.ResponseWriter, prefix string) {
	type content struct {
		Key  string
		Size int
	}
	var out struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		Contents    []content
		IsTruncated bool
	}
	for _, k := range f.Keys() {
		if strings.HasPrefix(k, prefix) {
			o, _ := f.Object(k)
			out.Contents = append(out.Contents, content{Key: k, Size: len(o.Body)})
		}
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

func (f *Fake) assumeRole(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if key != f.Instance.AccessKeyID {
		s3err(w, http.StatusForbidden, "AccessDenied", "only the instance role may assume the writer role")
		return
	}
	form, _ := url.ParseQuery(string(body))
	secs, _ := strconv.Atoi(form.Get("DurationSeconds"))
	if secs > 3600 {
		s3err(w, http.StatusBadRequest, "ValidationError", "The requested DurationSeconds exceeds the 1 hour session limit for roles assumed by role chaining.")
		return
	}
	var pol struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource any
		}
	}
	if err := json.Unmarshal([]byte(form.Get("Policy")), &pol); err != nil {
		s3err(w, http.StatusBadRequest, "MalformedPolicyDocument", err.Error())
		return
	}
	var resources []string
	for _, st := range pol.Statement {
		if st.Effect != "Allow" {
			continue
		}
		switch v := st.Resource.(type) {
		case string:
			resources = append(resources, v)
		case []any:
			for _, x := range v {
				resources = append(resources, fmt.Sprint(x))
			}
		}
	}
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("ASIASESSION%04d", f.n)
	exp := f.Now().Add(time.Duration(secs) * time.Second).UTC()
	s := session{secret: "session-secret-" + id, token: "session-token-" + id, resources: resources, expires: exp}
	f.sessions[id] = s
	f.Assumes = append(f.Assumes, Assumed{RoleARN: form.Get("RoleArn"), SessionName: form.Get("RoleSessionName"), Policy: form.Get("Policy"), Duration: time.Duration(secs) * time.Second})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>`+
		`<AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration>`+
		`</Credentials></AssumeRoleResult></AssumeRoleResponse>`, id, s.secret, s.token, exp.Format(time.RFC3339))
}

func sesErr(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Errortype", typ+":http://internal.amazon.com/coral/com.amazonaws.sesv2/")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

func (f *Fake) sendEmail(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if r.Method != http.MethodPost || r.URL.Path != "/ses/v2/email/outbound-emails" {
		sesErr(w, http.StatusNotFound, "NotFoundException", r.Method+" "+r.URL.Path)
		return
	}
	if key != f.Instance.AccessKeyID {
		sesErr(w, http.StatusForbidden, "AccessDeniedException", "not authorized to perform ses:SendEmail")
		return
	}
	var req struct {
		FromEmailAddress string
		Destination      struct{ ToAddresses []string }
		Content          struct {
			Raw *struct{ Data []byte }
		}
		ConfigurationSetName string
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Content.Raw == nil || len(req.Destination.ToAddresses) == 0 {
		sesErr(w, http.StatusBadRequest, "BadRequestException", fmt.Sprintf("malformed SendEmail request: %v", err))
		return
	}
	if f.SESError != "" {
		sesErr(w, http.StatusBadRequest, f.SESError, "refused by the fake")
		return
	}
	f.mu.Lock()
	f.n++
	f.Emails = append(f.Emails, Email{From: req.FromEmailAddress, To: req.Destination.ToAddresses,
		ConfigurationSet: req.ConfigurationSetName, Raw: req.Content.Raw.Data})
	id := fmt.Sprintf("0100018f-fake-%04d", f.n)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"MessageId": id})
}

// authenticate re-signs the request with the secret of the key it claims
// and compares signatures, so a request that would not verify on AWS (wrong
// canonical form, a header the client did not send) fails here too.
func (f *Fake) authenticate(r *http.Request, body []byte) (string, error) {
	service := "s3"
	if strings.HasPrefix(r.URL.Path, "/sts/") {
		service = "sts"
	} else if strings.HasPrefix(r.URL.Path, "/ses/") {
		service = "ses"
	}
	signer := awsv4.Signer{Region: f.Region, Service: service}
	q := r.URL.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "" {
		cred := strings.SplitN(q.Get("X-Amz-Credential"), "/", 2)[0]
		c, err := f.creds(cred, q.Get("X-Amz-Security-Token"))
		if err != nil {
			return "", err
		}
		at, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
		if err != nil {
			return "", err
		}
		secs, _ := strconv.Atoi(q.Get("X-Amz-Expires"))
		if !f.Now().Before(at.Add(time.Duration(secs) * time.Second)) {
			return "", fmt.Errorf("request has expired")
		}
		hdr := http.Header{}
		for _, h := range strings.Split(q.Get("X-Amz-SignedHeaders"), ";") {
			if h != "host" {
				if r.Header.Get(h) == "" {
					return "", fmt.Errorf("signed header %s not sent", h)
				}
				hdr.Set(h, r.Header.Get(h))
			}
		}
		for _, k := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Security-Token", "X-Amz-Signature"} {
			q.Del(k)
		}
		u := *r.URL
		u.Scheme, u.Host = "http", r.Host
		u.RawQuery = q.Encode()
		want, _ := url.Parse(signer.Presign(r.Method, &u, c, hdr, time.Duration(secs)*time.Second, at))
		if want.Query().Get("X-Amz-Signature") != sig {
			return "", fmt.Errorf("presigned signature does not match")
		}
		return cred, nil
	}
	auth := r.Header.Get("Authorization")
	credPart, _, _ := strings.Cut(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 Credential="), "/")
	_, signedPart, _ := strings.Cut(auth, "SignedHeaders=")
	signedPart, _, _ = strings.Cut(signedPart, ",")
	c, err := f.creds(credPart, r.Header.Get("X-Amz-Security-Token"))
	if err != nil {
		return "", err
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "", fmt.Errorf("x-amz-date: %w", err)
	}
	payload := awsv4.HashHex(body)
	if service == "s3" {
		if got := r.Header.Get("X-Amz-Content-Sha256"); got != payload {
			return "", fmt.Errorf("x-amz-content-sha256 does not match the body")
		}
	}
	re, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	for _, h := range strings.Split(signedPart, ";") {
		switch h {
		case "host", "x-amz-date", "x-amz-security-token", "x-amz-content-sha256":
		default:
			re.Header.Set(h, r.Header.Get(h))
		}
	}
	signer.Sign(re, c, payload, at)
	if re.Header.Get("Authorization") != auth {
		return "", fmt.Errorf("signature does not match")
	}
	return credPart, nil
}

func (f *Fake) creds(id, token string) (awsv4.Credentials, error) {
	if id == f.Instance.AccessKeyID {
		if token != f.Instance.SessionToken {
			return awsv4.Credentials{}, fmt.Errorf("bad session token")
		}
		return f.Instance, nil
	}
	f.mu.Lock()
	s, ok := f.sessions[id]
	f.mu.Unlock()
	if !ok {
		return awsv4.Credentials{}, fmt.Errorf("unknown key %s", id)
	}
	if token != s.token {
		return awsv4.Credentials{}, fmt.Errorf("bad session token")
	}
	return awsv4.Credentials{AccessKeyID: id, SecretAccessKey: s.secret, SessionToken: s.token}, nil
}
