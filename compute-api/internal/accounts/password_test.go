package accounts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// cheapParams keeps tests fast; DefaultParams is exercised once below.
var cheapParams = Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestHashRoundTripWithDefaultParams(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := NewHasher(DefaultParams)
	enc, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("not the PHC form with OWASP parameters: %s", enc)
	}
	ok, rehash, err := h.Verify(ctx, "correct horse battery", enc)
	if err != nil || !ok || rehash {
		t.Fatalf("verify own hash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, _, err = h.Verify(ctx, "correct horse batterY", enc)
	if err != nil || ok {
		t.Fatalf("wrong password verified: ok=%v err=%v", ok, err)
	}
	other, _ := h.Hash(ctx, "correct horse battery")
	if other == enc {
		t.Fatal("two hashes of one password are equal; salt not random")
	}
}

func TestVerifyAsksForRehashWhenParamsChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := NewHasher(cheapParams)
	enc, err := old.Hash(ctx, "a password long enough")
	if err != nil {
		t.Fatal(err)
	}
	raised := cheapParams
	raised.Time = 2
	ok, rehash, err := NewHasher(raised).Verify(ctx, "a password long enough", enc)
	if err != nil || !ok || !rehash {
		t.Fatalf("old hash under new params: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, rehash, _ = NewHasher(raised).Verify(ctx, "not the password at all", enc)
	if ok || rehash {
		t.Fatalf("wrong password must not ask for a rehash: ok=%v rehash=%v", ok, rehash)
	}
}

func TestVerifyRefusesMalformedHashes(t *testing.T) {
	t.Parallel()
	h := NewHasher(cheapParams)
	for _, enc := range []string{
		"",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=16$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$$a2V5a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$!!",
		"$2a$10$bcryptbcryptbcryptbcrypt",
	} {
		if _, _, err := h.Verify(context.Background(), "x", enc); !errors.Is(err, errMalformedHash) {
			t.Errorf("%q: want errMalformedHash, got %v", enc, err)
		}
	}
}

func TestHashWaitsForAFreeSlot(t *testing.T) {
	t.Parallel()
	h := NewHasher(cheapParams)
	for range MaxConcurrentHashes {
		h.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hash with every slot taken: want deadline exceeded, got %v", err)
	}
	if err := h.VerifyDummy(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dummy verify with every slot taken: want deadline exceeded, got %v", err)
	}
	<-h.slots
	if _, err := h.Hash(context.Background(), "x"); err != nil {
		t.Fatalf("hash after a slot frees: %v", err)
	}
}

func TestVerifyDummyCostsAVerification(t *testing.T) {
	t.Parallel()
	h := NewHasher(cheapParams)
	if err := h.VerifyDummy(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	if h.dummy == "" || !strings.HasPrefix(h.dummy, "$argon2id$") {
		t.Fatalf("dummy hash not made with the hasher: %q", h.dummy)
	}
}
