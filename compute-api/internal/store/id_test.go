package store

import (
	"regexp"
	"testing"
	"time"
)

var jobIDPattern = regexp.MustCompile(`^job_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

func TestNewJobIDFormat(t *testing.T) {
	seen := map[string]bool{}
	now := time.Now()
	for range 1000 {
		id := NewJobID(now)
		if !jobIDPattern.MatchString(id) {
			t.Fatalf("id %q does not match %s", id, jobIDPattern)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestNewJobIDSortsByMillisecond(t *testing.T) {
	base := time.UnixMilli(1757520000000)
	prev := NewJobID(base)
	for i := 1; i < 200; i++ {
		id := NewJobID(base.Add(time.Duration(i) * time.Millisecond))
		if id <= prev {
			t.Fatalf("id at +%dms %q does not sort after %q", i, id, prev)
		}
		prev = id
	}
}

// Pins the encoding to the ULID spec's layout: an all-zero value is all
// zeros, and the maximum 128-bit value is the documented maximum ULID.
func TestEncodeULIDBounds(t *testing.T) {
	if got := encodeULID([16]byte{}); got != "00000000000000000000000000" {
		t.Fatalf("zero: got %q", got)
	}
	var max [16]byte
	for i := range max {
		max[i] = 0xff
	}
	if got := encodeULID(max); got != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("max: got %q", got)
	}
	ts := [16]byte{0x01, 0x99, 0x3b, 0x2f, 0x4c, 0x00}
	if got := encodeULID(ts)[:10]; got != "01K4XJYK00" {
		t.Fatalf("timestamp prefix: got %q", got)
	}
}
