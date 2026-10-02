package ratelimit

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func hash(k string) [sha256.Size]byte    { return sha256.Sum256([]byte(k)) }

func near(got, want time.Duration) bool {
	return got > want-time.Millisecond && got < want+time.Millisecond
}

func newAt(burst int, w time.Duration) (*Limiter, *clock) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	l := New(burst, w)
	l.now = c.now
	return l, c
}

func TestSubmitBudgetIsSixtyThenOnePerMinute(t *testing.T) {
	l, c := newAt(60, time.Hour)
	for i := range 60 {
		if ok, _ := l.Allow("sk-a"); !ok {
			t.Fatalf("request %d refused inside the burst", i+1)
		}
	}
	ok, wait := l.Allow("sk-a")
	if ok || !near(wait, time.Minute) {
		t.Fatalf("61st: ok=%v retryAfter=%v, want refused for 1m", ok, wait)
	}
	c.advance(30 * time.Second)
	if ok, wait := l.Allow("sk-a"); ok || !near(wait, 30*time.Second) {
		t.Fatalf("after 30s: ok=%v retryAfter=%v, want refused for 30s", ok, wait)
	}
	c.advance(30 * time.Second)
	if ok, _ := l.Allow("sk-a"); !ok {
		t.Fatal("one token should have refilled after a minute")
	}
	if ok, _ := l.Allow("sk-a"); ok {
		t.Fatal("only one token refills per minute")
	}
}

func TestKeysHaveSeparateBudgets(t *testing.T) {
	l, _ := newAt(1, time.Minute)
	if ok, _ := l.Allow("sk-a"); !ok {
		t.Fatal()
	}
	if ok, _ := l.Allow("sk-a"); ok {
		t.Fatal("sk-a should be out")
	}
	if ok, _ := l.Allow("sk-b"); !ok {
		t.Fatal("sk-b must not share sk-a's budget")
	}
}

func TestSweepForgetsOnlyFullBuckets(t *testing.T) {
	l, c := newAt(600, time.Minute)
	l.Allow("sk-idle")
	for range 600 {
		l.Allow("sk-busy")
	}
	c.advance(time.Second) // idle has refilled; busy has 10 tokens back
	l.Allow("sk-other")
	if _, ok := l.buckets[hash("sk-idle")]; ok {
		t.Fatal("a refilled bucket should have been swept")
	}
	if _, ok := l.buckets[hash("sk-busy")]; !ok {
		t.Fatal("a partly spent bucket must survive the sweep, or its budget resets")
	}
}

func TestMemoryIsBoundedUnderAFloodOfKeys(t *testing.T) {
	l, _ := newAt(60, time.Hour)
	for i := range maxKeys + 10 {
		l.Allow("sk-" + strconv.Itoa(i))
	}
	if n := len(l.buckets); n > maxKeys {
		t.Fatalf("%d buckets, cap is %d", n, maxKeys)
	}
}

func TestConcurrentUseSpendsEachTokenOnce(t *testing.T) {
	l := New(100, time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 400 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Allow("sk-shared"); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 100 {
		t.Fatalf("%d allowed, want exactly the burst of 100", allowed)
	}
}
