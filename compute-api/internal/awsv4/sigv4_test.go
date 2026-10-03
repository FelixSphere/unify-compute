package awsv4

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The examples in the S3 API reference, "Signature Calculations for the
// Authorization Header: Transferring Payload in a Single Chunk" and
// "Authenticating Requests: Using Query Parameters". Same key, same instant,
// same expected signatures as published there.
var (
	docCreds = Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	docTime  = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	docS3    = Signer{Region: "us-east-1", Service: "s3"}
)

func signatureOf(t *testing.T, auth string) string {
	t.Helper()
	_, sig, ok := strings.Cut(auth, "Signature=")
	if !ok {
		t.Fatalf("no signature in %q", auth)
	}
	return sig
}

func TestSignMatchesS3DocumentedExamples(t *testing.T) {
	cases := []struct {
		name, method, url, payload, signed, want string
		headers                                  map[string]string
	}{
		{
			name: "GET object", method: "GET", url: "https://examplebucket.s3.amazonaws.com/test.txt",
			payload: EmptySHA256, headers: map[string]string{"Range": "bytes=0-9"},
			signed: "host;range;x-amz-content-sha256;x-amz-date",
			want:   "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "PUT object", method: "PUT", url: "https://examplebucket.s3.amazonaws.com/test%24file.text",
			payload: HashHex([]byte("Welcome to Amazon S3.")),
			headers: map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"},
			signed:  "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			want:    "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "GET bucket lifecycle", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?lifecycle",
			payload: EmptySHA256, signed: "host;x-amz-content-sha256;x-amz-date",
			want: "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "list objects", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			payload: EmptySHA256, signed: "host;x-amz-content-sha256;x-amz-date",
			want: "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, c.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			docS3.Sign(req, docCreds, c.payload, docTime)
			auth := req.Header.Get("Authorization")
			if !strings.Contains(auth, "SignedHeaders="+c.signed+",") {
				t.Errorf("signed headers: %s", auth)
			}
			if got := signatureOf(t, auth); got != c.want {
				t.Errorf("signature %s, want %s", got, c.want)
			}
		})
	}
}

func TestPresignMatchesS3DocumentedExample(t *testing.T) {
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	got := docS3.Presign("GET", u, docCreds, nil, 86400*time.Second, docTime)
	pu, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := pu.Query()
	if s := q.Get("X-Amz-Signature"); s != "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404" {
		t.Fatalf("signature %s in %s", s, got)
	}
	if q.Get("X-Amz-Credential") != "AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request" || q.Get("X-Amz-SignedHeaders") != "host" {
		t.Fatalf("query %v", q)
	}
}

// A presigned URL that names a header the client must send puts it in
// SignedHeaders and carries the session token, which S3 requires to be
// part of the signed query for temporary credentials.
func TestPresignSignsRequiredHeadersAndSessionToken(t *testing.T) {
	u, _ := url.Parse("https://b.s3.us-east-1.amazonaws.com/jobs/job_1/out/a%20b.txt")
	c := docCreds
	c.SessionToken = "tok/en+=="
	h := http.Header{}
	h.Set("X-Amz-Checksum-Sha256", "abc=")
	got := docS3.Presign("PUT", u, c, h, time.Hour, docTime)
	pu, _ := url.Parse(got)
	q := pu.Query()
	if q.Get("X-Amz-SignedHeaders") != "host;x-amz-checksum-sha256" || q.Get("X-Amz-Security-Token") != "tok/en+==" || q.Get("X-Amz-Expires") != "3600" {
		t.Fatalf("query %v", q)
	}
	if !strings.HasPrefix(got, "https://b.s3.us-east-1.amazonaws.com/jobs/job_1/out/a%20b.txt?") {
		t.Fatalf("path changed: %s", got)
	}
}

func TestEscapeKeyKeepsSlashesAndEncodesTheRest(t *testing.T) {
	if got := EscapeKey("jobs/job_1/mnt/out/a b+c$~.txt"); got != "jobs/job_1/mnt/out/a%20b%2Bc%24~.txt" {
		t.Fatal(got)
	}
}
