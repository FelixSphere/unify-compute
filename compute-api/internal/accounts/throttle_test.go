package accounts

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestThrottleWindowOpensAtFirstAttemptAndIsNotExtended(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	r := Rule{"test_fixed", 3, 10 * time.Minute}

	for i := 1; i <= 3; i++ {
		d, err := f.svc.Hit(ctx, r, "203.0.113.1")
		if err != nil || !d.Allowed || d.Count != i {
			t.Fatalf("attempt %d: %+v %v", i, d, err)
		}
		f.clock.advance(time.Minute)
	}
	d, _ := f.svc.Hit(ctx, r, "203.0.113.1")
	if d.Allowed || d.RetryAfter != 7*time.Minute {
		t.Fatalf("4th attempt 3 min into the window: %+v, want refused with 7m left", d)
	}
	// Refused attempts are counted but do not push the window out.
	f.clock.advance(6*time.Minute + 59*time.Second + 500*time.Millisecond)
	d, _ = f.svc.Hit(ctx, r, "203.0.113.1")
	if d.Allowed || d.RetryAfter != time.Second {
		t.Fatalf("half a second before the window closes: %+v, want refused with Retry-After 1s", d)
	}
	f.clock.advance(500 * time.Millisecond)
	d, _ = f.svc.Hit(ctx, r, "203.0.113.1")
	if !d.Allowed || d.Count != 1 {
		t.Fatalf("first attempt of a new window: %+v", d)
	}

	if d, _ := f.svc.Hit(ctx, r, "203.0.113.2"); !d.Allowed || d.Count != 1 {
		t.Fatalf("another subject shares the counter: %+v", d)
	}
}

// Unify Data's bug: one purge used one window for every row, so a route with
// a 1-minute window reset the counters of a route with a 1-hour window.
func TestShortWindowDoesNotResetLongWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	const email = "victim@example.com"

	for i := range ResendPerEmailHour.Limit {
		if d, _ := f.svc.Hit(ctx, ResendPerEmailMinute, email); !d.Allowed {
			t.Fatalf("resend %d refused by the minute rule: %+v", i+1, d)
		}
		if d, _ := f.svc.Hit(ctx, ResendPerEmailHour, email); !d.Allowed {
			t.Fatalf("resend %d refused by the hour rule: %+v", i+1, d)
		}
		f.clock.advance(2 * time.Minute)
		if err := f.svc.PurgeExpired(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Ten minutes in: the minute rule's window has passed and its row is
	// purged; the hour rule's must still hold.
	if d, _ := f.svc.Hit(ctx, ResendPerEmailMinute, email); !d.Allowed || d.Count != 1 {
		t.Fatalf("minute rule after its window: %+v", d)
	}
	d, err := f.svc.Hit(ctx, ResendPerEmailHour, email)
	if err != nil || d.Allowed {
		t.Fatalf("hour rule reset by the minute rule's purge: %+v %v", d, err)
	}
	if d.RetryAfter != 50*time.Minute {
		t.Fatalf("hour rule's window moved: Retry-After %v, want 50m", d.RetryAfter)
	}

	f.clock.advance(50 * time.Minute)
	if err := f.svc.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM auth_attempts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows left after both windows passed: %d %v", rows, err)
	}
}

func TestThrottleCheckDoesNotCount(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	r := Rule{"test_check", 2, time.Hour}

	if d, _ := f.svc.Check(ctx, r, "x"); !d.Allowed || d.Count != 0 {
		t.Fatalf("check with no row: %+v", d)
	}
	f.svc.Hit(ctx, r, "x")
	for range 5 {
		if d, _ := f.svc.Check(ctx, r, "x"); !d.Allowed || d.Count != 1 {
			t.Fatalf("check after one hit: %+v", d)
		}
	}
	f.svc.Hit(ctx, r, "x")
	d, _ := f.svc.Check(ctx, r, "x")
	if d.Allowed || d.RetryAfter != time.Hour {
		t.Fatalf("check at the limit: %+v", d)
	}
	f.clock.advance(time.Hour)
	if d, _ := f.svc.Check(ctx, r, "x"); !d.Allowed || d.Count != 0 {
		t.Fatalf("check after the window, row not yet purged: %+v", d)
	}
}

func TestConcurrentHitsAreAllCounted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	r := Rule{"test_race", 10, time.Hour}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := f.svc.Hit(ctx, r, "x")
			if err != nil {
				t.Errorf("hit: %v", err)
				return
			}
			if d.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != r.Limit {
		t.Fatalf("%d of 25 concurrent attempts allowed, want %d", allowed, r.Limit)
	}
}

func TestThrottleBucketsAreDistinct(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, r := range []Rule{LoginPerIP, LoginPerAccount, SignupPerIP, ForgotPerIP, VerifyPerIP,
		ResendPerEmailMinute, ResendPerEmailHour} {
		if seen[r.Bucket] {
			t.Fatalf("bucket %q used by two rules; their counters would merge", r.Bucket)
		}
		seen[r.Bucket] = true
	}
}
