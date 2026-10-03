// Package ratelimit is the per-key request limiter of docs/spec/job-api.md
// (60 submits per hour, 600 reads per minute). It is a token bucket per key,
// in process memory: with several replicas each enforces its own budget, so
// the effective limit is the per-process limit times the replica count.
package ratelimit

import (
	"crypto/sha256"
	"math"
	"sync"
	"time"
)

// maxKeys bounds memory while auth only checks a key's shape: until the
// console bridge verifies keys, anyone can mint fresh ones, and each would
// otherwise keep a bucket alive for a whole window.
const maxKeys = 100_000

type bucket struct {
	tokens float64
	at     time.Time
}

type Limiter struct {
	burst float64
	rate  float64 // tokens per second
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[[sha256.Size]byte]*bucket
	lastSweep time.Time
}

// New allows burst requests per key, refilled evenly over window: New(60,
// time.Hour) is 60 at once, then one a minute.
func New(burst int, window time.Duration) *Limiter {
	return &Limiter{
		burst:   float64(burst),
		rate:    float64(burst) / window.Seconds(),
		now:     time.Now,
		buckets: map[[sha256.Size]byte]*bucket{},
	}
}

// Allow spends one token for key. When none is left it returns false and how
// long until one is. Keys are stored hashed so the limiter never holds a
// customer's credential.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	id := sha256.Sum256([]byte(key))
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, found := l.buckets[id]
	if !found {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[id] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// sweep drops buckets that have refilled completely, since a missing bucket
// and a full one behave the same. It runs at most once per refill period of
// one token, so its cost is amortised over many calls.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep).Seconds()*l.rate < 1 && len(l.buckets) < maxKeys {
		return
	}
	l.lastSweep = now
	for id, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.rate >= l.burst {
			delete(l.buckets, id)
		}
	}
	// Still over the cap means a flood of distinct keys inside one period.
	// Forgetting everyone resets real keys' budgets too, which is the
	// lesser harm than unbounded memory; it stops mattering once keys are
	// verified and cannot be minted.
	if len(l.buckets) >= maxKeys {
		clear(l.buckets)
	}
}
