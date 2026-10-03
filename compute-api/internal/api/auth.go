package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
)

// VerifyTTL is how long a verify answer is reused (job-api.md,
// "Authentication"): a revoked key stops working within this window.
const VerifyTTL = 60 * time.Second

// maxCachedKeys bounds the cache. Rejections are cached too, so a flood of
// made-up keys reaches the console once per key per minute at most; past the
// bound the cache starts over rather than grow.
const maxCachedKeys = 100_000

var (
	// ErrConsoleUnavailable: the key could not be checked at all (console
	// unreachable, bridge dark, or our own credential refused).
	ErrConsoleUnavailable = errors.New("api: console bridge unavailable")
)

// AccountRefusedError is a valid key whose account may not spend: the
// bridge's 403 with data.status "disabled" (user) or "suspended" (tenant).
type AccountRefusedError struct{ Status string }

func (e *AccountRefusedError) Error() string { return "api: account " + e.Status }

// Verifier resolves customer keys through the console bridge's verify.
type Verifier struct {
	Console *console.Client
	Log     *slog.Logger
	Now     func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]verified
}

type verified struct {
	id    console.Identity
	err   error
	until time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify returns the key's identity, ErrKeyRejected, *AccountRefusedError,
// or ErrConsoleUnavailable. Only the first three are cached: an outage must
// not outlive itself in the cache.
func (v *Verifier) Verify(ctx context.Context, key string) (console.Identity, error) {
	h := sha256.Sum256([]byte(key))
	now := v.now()
	v.mu.Lock()
	c, ok := v.cache[h]
	v.mu.Unlock()
	if ok && now.Before(c.until) {
		return c.id, c.err
	}

	if v.Console.BaseURL == "" {
		// No console configured (CONSOLE_URL unset): expected, not an error
		// worth a log line per request.
		return console.Identity{}, ErrConsoleUnavailable
	}
	var res verified
	id, err := v.Console.Verify(ctx, key)
	var ce *console.Error
	switch {
	case err == nil:
		res.id = *id
	case errors.As(err, &ce) && ce.Status == http.StatusUnauthorized && ce.Message == "invalid_key":
		res.err = ErrKeyRejected
	case errors.As(err, &ce) && ce.Status == http.StatusForbidden:
		status := ce.AccountStatus
		if status == "" {
			status = ce.Message
		}
		res.err = &AccountRefusedError{Status: status}
	default:
		// A 401 other than invalid_key is the bridge refusing our own
		// signature or clock: an operator problem, never the customer's.
		if v.Log != nil {
			v.Log.Error("console verify", "err", err)
		}
		return console.Identity{}, ErrConsoleUnavailable
	}
	res.until = now.Add(VerifyTTL)
	v.mu.Lock()
	if v.cache == nil || len(v.cache) >= maxCachedKeys {
		fresh := make(map[[32]byte]verified)
		for k, e := range v.cache {
			if now.Before(e.until) && len(fresh) < maxCachedKeys/2 {
				fresh[k] = e
			}
		}
		v.cache = fresh
	}
	v.cache[h] = res
	v.mu.Unlock()
	return res.id, res.err
}

// Owner adapts Verify to Server.Owner.
func (v *Verifier) Owner(ctx context.Context, key string) (string, error) {
	id, err := v.Verify(ctx, key)
	if err != nil {
		return "", err
	}
	return id.Owner(), nil
}
