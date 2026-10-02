package store

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewJobID returns "job_" followed by a 26-character ULID: 48 bits of Unix
// milliseconds then 80 random bits, Crockford base32. IDs sort by creation
// time to the millisecond; within one millisecond their order is arbitrary,
// which is why listing orders by created_at and uses the id only as a
// tie-break.
func NewJobID(now time.Time) string { return NewID("job_", now) }

// NewID is NewJobID with another prefix, for the other ids compute-api sends
// the console (hold_, ue_, ext_) and that must never collide.
func NewID(prefix string, now time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMilli())<<16)
	_, _ = rand.Read(b[6:])
	return prefix + encodeULID(b)
}

func encodeULID(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	// 26 chars × 5 bits = 130 bits; the value is 128 bits with two implied
	// leading zeros, so the first character is always 0-7.
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&0x1f]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
