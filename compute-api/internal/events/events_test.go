package events

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestValidateRequiredFields(t *testing.T) {
	ok := []struct {
		t    Type
		data string
	}{
		{JobQueued, `{"sku":"a10g-24gb-x1"}`},
		{JobProvisioning, ``},
		{JobRunning, `{}`},
		{JobPlaced, `{"placement":{"cloud":"aws","region":"us-east-1","instance_type":"g5.xlarge"}}`},
		{JobFailover, `{"from":"aws","to":"gcp","reason":"capacity_exhausted"}`},
		{JobPreempted, `{"provider":"aws"}`},
		{JobSucceeded, `{}`},
		{JobFailed, `{"error":{"code":"capacity_exhausted","message":"no capacity"},"retryable":true}`},
		{JobFailed, `{"error":{"code":"image_pull_failed"},"retryable":false}`},
		{JobCancelled, `{"reason":"balance_exhausted"}`},
		{HoldExtended, `{"hold_usd":"2.5400","expires_at":1757527200}`},
		{BalanceExhausted, `{}`},
	}
	for _, c := range ok {
		if err := Validate(c.t, json.RawMessage(c.data)); err != nil {
			t.Errorf("%s %s: %v", c.t, c.data, err)
		}
	}
	bad := []struct {
		t    Type
		data string
	}{
		{"job.exploded", `{}`},
		{JobSucceeded, `[]`},
		{JobSucceeded, `null`},
		{JobQueued, `{}`},
		{JobPlaced, `{"placement":null}`},
		{JobFailover, `{"from":"aws"}`},
		{JobPreempted, `{}`},
		{JobFailed, `{"retryable":true}`},
		{JobFailed, `{"error":{"code":"image_pull_failed"}}`},
		{JobCancelled, `{}`},
		{HoldExtended, `{"expires_at":1}`},
	}
	for _, c := range bad {
		if err := Validate(c.t, json.RawMessage(c.data)); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s %s: got %v, want ErrInvalidEvent", c.t, c.data, err)
		}
	}
}

func TestDefaultWebhookTypesAreTheTerminalOnes(t *testing.T) {
	want := map[Type]bool{JobSucceeded: true, JobFailed: true, JobCancelled: true, JobPreempted: true}
	if len(DefaultWebhookTypes) != len(want) {
		t.Fatalf("defaults %v", DefaultWebhookTypes)
	}
	for _, d := range DefaultWebhookTypes {
		if !want[d] {
			t.Errorf("%s is not terminal", d)
		}
	}
}

// Pins the schedule docs/spec/events.md publishes: 1 min doubling to a 6 h
// cap, and a give-up point about 20.5 hours after the event.
func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 360, 360}
	var total time.Duration
	for i, m := range want {
		if got := Backoff(i + 1); got != m*time.Minute {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, m*time.Minute)
		}
		total += Backoff(i + 1)
	}
	if len(want) != MaxAttempts-1 {
		t.Fatalf("schedule has %d waits for %d attempts", len(want), MaxAttempts)
	}
	if total != 1231*time.Minute {
		t.Errorf("time to give up %v, want 20h31m", total)
	}
	if Backoff(40) != 6*time.Hour {
		t.Errorf("Backoff(40) = %v", Backoff(40))
	}
}

func TestNewSecretShape(t *testing.T) {
	a, b := NewSecret(), NewSecret()
	if len(a) != len("whsec_")+64 || a[:6] != "whsec_" || a == b {
		t.Fatalf("secrets %q %q", a, b)
	}
}
