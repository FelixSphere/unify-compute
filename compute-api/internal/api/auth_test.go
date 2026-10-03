package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
)

func TestVerifierCachesAnswersForAMinuteAndOutagesNotAtAll(t *testing.T) {
	fc := consoletest.New([]byte(e2eSecret))
	t.Cleanup(fc.Close)
	clock := &e2eClock{t: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)}
	fc.SetNow(clock.Now)
	fc.SeedUser(consoletest.User{ID: 1, Quota: 1})
	fc.SeedKey(consoletest.Key{Key: "sk-good-0000000", TokenID: 5, UserID: 1})
	v := &Verifier{Console: fc.Client(), Now: clock.Now}
	ctx := context.Background()
	calls := func() int { return len(fc.CallsTo(consoletest.RouteVerify)) }

	id, err := v.Verify(ctx, "sk-good-0000000")
	if err != nil || id.Owner() != "user:1" || id.TokenID != 5 {
		t.Fatalf("verify: %+v %v", id, err)
	}
	clock.Advance(VerifyTTL - time.Second)
	if _, err := v.Verify(ctx, "sk-good-0000000"); err != nil || calls() != 1 {
		t.Fatalf("within the TTL: %v, %d calls", err, calls())
	}

	// A key revoked on the console stops working once the TTL has passed.
	fc.SeedKey(consoletest.Key{Key: "sk-good-0000000", TokenID: 5, UserID: 1, Disabled: true})
	clock.Advance(2 * time.Second)
	if _, err := v.Verify(ctx, "sk-good-0000000"); !errors.Is(err, ErrKeyRejected) || calls() != 2 {
		t.Fatalf("after revocation: %v, %d calls", err, calls())
	}
	if _, err := v.Verify(ctx, "sk-good-0000000"); !errors.Is(err, ErrKeyRejected) || calls() != 2 {
		t.Fatalf("a rejection is cached too: %v, %d calls", err, calls())
	}

	fc.SetDown(true)
	if _, err := v.Verify(ctx, "sk-other-000000"); !errors.Is(err, ErrConsoleUnavailable) {
		t.Fatalf("console down: %v", err)
	}
	fc.SetDown(false)
	fc.SeedKey(consoletest.Key{Key: "sk-other-000000", TokenID: 6, UserID: 1})
	if _, err := v.Verify(ctx, "sk-other-000000"); err != nil {
		t.Fatalf("an outage must not be cached: %v", err)
	}
}
