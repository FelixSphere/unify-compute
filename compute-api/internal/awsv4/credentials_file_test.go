package awsv4_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
)

// The body IMDS returns for a role, which the host timer copies verbatim.
func imdsBody(key string, exp time.Time) string {
	return `{
  "Code" : "Success",
  "LastUpdated" : "2026-10-02T06:00:00Z",
  "Type" : "AWS-HMAC",
  "AccessKeyId" : "` + key + `",
  "SecretAccessKey" : "s",
  "Token" : "t",
  "Expiration" : "` + exp.Format(time.RFC3339) + `"
}`
}

func TestFileRereadsNearExpiryAndKeepsValidCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path+".new", []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	exp := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	now := exp.Add(-time.Hour)
	f := &awsv4.File{Path: path, Now: func() time.Time { return now }}
	ctx := context.Background()

	if _, err := f.Credentials(ctx); err == nil {
		t.Fatal("no file yet, but no error")
	}

	write(imdsBody("ASIA1", exp))
	c, err := f.Credentials(ctx)
	if err != nil || c.AccessKeyID != "ASIA1" || c.SessionToken != "t" || !c.Expires.Equal(exp) {
		t.Fatalf("%+v %v", c, err)
	}

	// Cached: a newer file is not read while the old set has time left.
	write(imdsBody("ASIA2", exp.Add(6*time.Hour)))
	if c, _ := f.Credentials(ctx); c.AccessKeyID != "ASIA1" {
		t.Fatalf("re-read too early: %s", c.AccessKeyID)
	}

	// Within five minutes of expiry the file is read again.
	now = exp.Add(-4 * time.Minute)
	if c, _ := f.Credentials(ctx); c.AccessKeyID != "ASIA2" {
		t.Fatalf("not re-read near expiry: %s", c.AccessKeyID)
	}

	// A broken file does not discard credentials that are still valid.
	write("{")
	now = exp.Add(6*time.Hour - 4*time.Minute)
	if c, err := f.Credentials(ctx); err != nil || c.AccessKeyID != "ASIA2" {
		t.Fatalf("%+v %v", c, err)
	}

	// A file the timer stopped refreshing fails once its credentials expire.
	write(imdsBody("ASIA2", exp.Add(6*time.Hour)))
	now = exp.Add(7 * time.Hour)
	if _, err := f.Credentials(ctx); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileRefusesAFailedIMDSAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"Code":"AssumeRoleUnauthorizedAccess","Message":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &awsv4.File{Path: path}
	if _, err := f.Credentials(context.Background()); err == nil || !strings.Contains(err.Error(), "AssumeRoleUnauthorizedAccess") {
		t.Fatalf("err = %v", err)
	}
}
