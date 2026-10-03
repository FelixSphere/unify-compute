package awsv4_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
)

const writerARN = "arn:aws:iam::123456789012:role/unify-compute-staging-artifacts-writer"

func policyFor(prefix string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject"],"Resource":"arn:aws:s3:::bkt/` + prefix + `*"}]}`
}

func TestAssumeRoleIsCappedAtOneHourLikeRoleChaining(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	c := f.Client()
	_, err := c.AssumeRole(context.Background(), f.Instance, writerARN, "job_1", policyFor("jobs/job_1/"), 2*time.Hour)
	var ae *awsv4.Error
	if !errors.As(err, &ae) || ae.Code != "ValidationError" {
		t.Fatalf("err = %v", err)
	}
	s, err := c.AssumeRole(context.Background(), f.Instance, writerARN, "job_1", policyFor("jobs/job_1/"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionToken == "" || s.Expires.IsZero() || len(f.Assumes) != 1 || f.Assumes[0].RoleARN != writerARN || f.Assumes[0].SessionName != "job_1" {
		t.Fatalf("session %+v assumes %+v", s, f.Assumes)
	}
}

func TestSessionWritesOnlyUnderItsPrefixAndInstanceReads(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	c := f.Client()
	ctx := context.Background()
	s, err := c.AssumeRole(ctx, f.Instance, writerARN, "job_1", policyFor("jobs/job_1/"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, s, "bkt", "jobs/job_1/_manifest.json", "application/json", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, s, "bkt", "jobs/job_2/x", "text/plain", []byte(`x`)); err == nil {
		t.Fatal("session wrote outside its prefix")
	}
	if err := c.Put(ctx, f.Instance, "bkt", "jobs/job_1/y", "text/plain", []byte(`y`)); err == nil {
		t.Fatal("instance key wrote")
	}
	b, err := c.Get(ctx, f.Instance, "bkt", "jobs/job_1/_manifest.json")
	if err != nil || string(b) != `{}` {
		t.Fatalf("get %q %v", b, err)
	}
	if _, err := c.Get(ctx, f.Instance, "bkt", "jobs/job_1/missing"); !awsv4.IsNotFound(err) {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := c.HeadChecksum(ctx, f.Instance, "bkt", "jobs/job_1/missing"); !awsv4.IsNotFound(err) {
		t.Fatalf("head missing: %v", err)
	}
}

// A presigned PUT carrying x-amz-checksum-sha256 is accepted only with that
// header and a body that hashes to it, and HEAD reports the checksum back.
func TestPresignedPutWithChecksum(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	c := f.Client()
	ctx := context.Background()
	s, err := c.AssumeRole(ctx, f.Instance, writerARN, "job_1", policyFor("jobs/job_1/"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("hello, artifacts")
	sum := sha256.Sum256(body)
	b64 := base64.StdEncoding.EncodeToString(sum[:])
	key := "jobs/job_1/mnt/out/a b+c.txt"
	u := c.Presign(s, http.MethodPut, "bkt", key, http.Header{"X-Amz-Checksum-Sha256": {b64}}, time.Hour)

	put := func(b []byte, checksum string) int {
		req, _ := http.NewRequest(http.MethodPut, u, bytes.NewReader(b))
		if checksum != "" {
			req.Header.Set("X-Amz-Checksum-Sha256", checksum)
		}
		resp, err := f.Server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put(body, ""); code != http.StatusForbidden {
		t.Fatalf("without the signed header: %d", code)
	}
	if code := put([]byte("tampered"), b64); code != http.StatusBadRequest {
		t.Fatalf("wrong body: %d", code)
	}
	if code := put(body, b64); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	size, got, err := c.HeadChecksum(ctx, f.Instance, "bkt", key)
	if err != nil || size != int64(len(body)) || got != b64 {
		t.Fatalf("head %d %q %v", size, got, err)
	}
	objs, truncated, err := c.List(ctx, f.Instance, "bkt", "jobs/job_1/", 10)
	if err != nil || truncated || len(objs) != 1 || objs[0].Key != key {
		t.Fatalf("list %+v %v %v", objs, truncated, err)
	}
}

func TestIMDSCachesUntilFiveMinutesBeforeExpiry(t *testing.T) {
	var calls atomic.Int32
	exp := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte("tok"))
		case r.Header.Get("X-aws-ec2-metadata-token") != "tok":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/":
			_, _ = w.Write([]byte("unify-compute-staging\n"))
		case r.URL.Path == "/latest/meta-data/iam/security-credentials/unify-compute-staging":
			calls.Add(1)
			_, _ = w.Write([]byte(`{"Code":"Success","AccessKeyId":"ASIAX","SecretAccessKey":"s","Token":"t","Expiration":"` + exp.Format(time.RFC3339) + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	now := exp.Add(-time.Hour)
	m := &awsv4.IMDS{Endpoint: srv.URL, Now: func() time.Time { return now }}
	for range 3 {
		c, err := m.Credentials(context.Background())
		if err != nil || c.AccessKeyID != "ASIAX" || c.SessionToken != "t" || !c.Expires.Equal(exp) {
			t.Fatalf("%+v %v", c, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fetched %d times", calls.Load())
	}
	now = exp.Add(-4 * time.Minute)
	if _, err := m.Credentials(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatalf("refresh: %v, %d calls", err, calls.Load())
	}
}

func TestErrorCarriesAWSCode(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	bad := f.Instance
	bad.SecretAccessKey = "wrong"
	_, err := f.Client().Get(context.Background(), bad, "bkt", "jobs/x")
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("err = %v", err)
	}
}
