package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost parameters. They are encoded into every hash
// (PHC string form), so raising them later only means hashes made before
// verify with their own parameters and are replaced at the next login.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   uint32
	KeyLen    uint32
}

// DefaultParams is OWASP's minimum for argon2id: m = 19 MiB, t = 2, p = 1
// (ADR-0011 §8).
var DefaultParams = Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// MaxConcurrentHashes bounds argon2id's memory: 4 × 19 MiB on a 2 GB host,
// however many logins arrive at once.
const MaxConcurrentHashes = 4

var errMalformedHash = errors.New("accounts: malformed argon2id hash")

// Hasher hashes and verifies passwords. Every call, including the dummy
// verification for an unknown email, waits for one of MaxConcurrentHashes
// slots.
type Hasher struct {
	params Params
	slots  chan struct{}

	dummyMu sync.Mutex
	dummy   string
}

func NewHasher(p Params) *Hasher {
	return &Hasher{params: p, slots: make(chan struct{}, MaxConcurrentHashes)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns the PHC string: $argon2id$v=19$m=…,t=…,p=…$salt$key.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.MemoryKiB, h.params.Threads, h.params.KeyLen)
	h.release()
	return encodePHC(h.params, salt, key), nil
}

// Verify reports whether password matches encoded, and whether encoded was
// made with parameters other than the hasher's, so the caller can store a
// fresh hash while it still has the plaintext.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decodePHC(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	h.release()
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != h.params, nil
}

// VerifyDummy spends what Verify spends against a real hash and always
// fails. Login calls it for an unknown email so the response time does not
// say whether the address is registered.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	dummy, err := h.dummyHash(ctx)
	if err != nil {
		return err
	}
	_, _, err = h.Verify(ctx, password, dummy)
	return err
}

// dummyHash is made on first use, and a failure (a cancelled request
// waiting for a slot) is not remembered.
func (h *Hasher) dummyHash(ctx context.Context) (string, error) {
	h.dummyMu.Lock()
	defer h.dummyMu.Unlock()
	if h.dummy == "" {
		d, err := h.Hash(ctx, "unify-compute-dummy-password")
		if err != nil {
			return "", err
		}
		h.dummy = d
	}
	return h.dummy, nil
}

var b64 = base64.RawStdEncoding

func encodePHC(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decodePHC(s string) (Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, errMalformedHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return Params{}, nil, nil, errMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, errMalformedHash
	}
	if p.MemoryKiB == 0 || p.Time == 0 || p.Threads == 0 {
		return Params{}, nil, nil, errMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Params{}, nil, nil, errMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return Params{}, nil, nil, errMalformedHash
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}
