package awsv4_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
)

func TestSendRawEmailIsSignedForSESAndCarriesTheConfigurationSet(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	raw := []byte("From: a@b.c\r\nTo: d@e.f\r\nSubject: x\r\n\r\nhello\r\n")
	id, err := f.Client().SendRawEmail(context.Background(), f.Instance, "no-reply@compute.unifyai.us", []string{"d@e.f"}, raw, "unify-compute")
	if err != nil || id == "" {
		t.Fatalf("id %q err %v", id, err)
	}
	if len(f.Emails) != 1 || string(f.Emails[0].Raw) != string(raw) || f.Emails[0].ConfigurationSet != "unify-compute" ||
		f.Emails[0].From != "no-reply@compute.unifyai.us" || f.Emails[0].To[0] != "d@e.f" {
		t.Fatalf("%+v", f.Emails)
	}
}

func TestSendRawEmailErrorsCarrySESCode(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	c := f.Client()
	ctx := context.Background()

	s, err := c.AssumeRole(ctx, f.Instance, writerARN, "job_1", policyFor("jobs/job_1/"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.SendRawEmail(ctx, s, "no-reply@compute.unifyai.us", []string{"d@e.f"}, []byte("x"), "")
	var ae *awsv4.Error
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "AccessDeniedException" {
		t.Fatalf("a job session sent mail: %v", err)
	}

	f.SESError = "MessageRejected"
	_, err = c.SendRawEmail(ctx, f.Instance, "no-reply@compute.unifyai.us", []string{"d@e.f"}, []byte("x"), "")
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Code != "MessageRejected" || ae.Message != "refused by the fake" {
		t.Fatalf("err = %v", err)
	}
	if len(f.Emails) != 0 {
		t.Fatalf("%d emails recorded", len(f.Emails))
	}
}
