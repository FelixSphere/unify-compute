package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errFull = errors.New("full")

func admitUpTo(n int) AdmitFunc {
	return func(_ context.Context, usage []SKUUsage) error {
		active := 0
		for _, u := range usage {
			active += u.Active
		}
		if active >= n {
			return errFull
		}
		return nil
	}
}

func TestActiveUsageCountsOnlyActiveJobsOfTheOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	mustCreate(t, s, newJob("user:1"))
	running := mustCreate(t, s, newJob("user:1"))
	done := mustCreate(t, s, newJob("user:1"))
	other := newJob("user:1")
	other.SKU = "l4-24gb-x1"
	mustCreate(t, s, other)
	mustCreate(t, s, newJob("user:2"))

	for _, to := range []Status{StatusProvisioning, StatusRunning} {
		if _, err := s.UpdateStatus(ctx, running.ID, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateStatus(ctx, done.ID, StatusCancelled, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.ActiveUsage(ctx, "user:1")
	if err != nil {
		t.Fatal(err)
	}
	want := []SKUUsage{{SKU: "a10g-24gb-x1", Active: 2, Queued: 1}, {SKU: "l4-24gb-x1", Active: 1, Queued: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}

	none, err := s.ActiveUsage(ctx, "user:3")
	if err != nil || len(none) != 0 {
		t.Fatalf("unknown owner: %+v %v", none, err)
	}
}

func TestCreateAdmittedRefusalPersistsNothing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	_, _, err := s.CreateAdmitted(ctx, newJob("user:1"), &Idempotency{Key: "k", RequestHash: hashOf("a")}, admitUpTo(0))
	if !errors.Is(err, errFull) {
		t.Fatalf("err = %v, want the admit error unchanged", err)
	}
	var jobs, keys int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&jobs)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys`).Scan(&keys)
	if jobs != 0 || keys != 0 {
		t.Fatalf("refused submit left %d jobs and %d keys", jobs, keys)
	}
}

// The race the lock exists for: many submits for one owner with one slot
// left. Each admit sleeps between reading usage and returning, which is
// where a console hold will sit, so without the lock every goroutine would
// read the same usage and pass.
func TestCreateAdmittedConcurrentSubmitsCannotShareTheLastSlot(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, newJob("user:1"))

	const limit, attempts = 2, 8
	inner := admitUpTo(limit)
	slow := func(ctx context.Context, u []SKUUsage) error {
		err := inner(ctx, u)
		time.Sleep(20 * time.Millisecond)
		return err
	}

	var created, refused atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.CreateAdmitted(ctx, newJob("user:1"), nil, slow)
			switch {
			case errors.Is(err, errFull):
				refused.Add(1)
			case err != nil:
				errs <- err
			case ok:
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if created.Load() != 1 || refused.Load() != attempts-1 {
		t.Fatalf("created %d, refused %d; want 1 and %d", created.Load(), refused.Load(), attempts-1)
	}
	usage, _ := s.ActiveUsage(ctx, "user:1")
	if len(usage) != 1 || usage[0].Active != limit {
		t.Fatalf("usage after race = %+v, want %d active", usage, limit)
	}
}

func TestCreateAdmittedDoesNotSerialiseOtherOwners(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := s.CreateAdmitted(ctx, newJob("user:1"), nil, func(context.Context, []SKUUsage) error {
			close(entered)
			<-release
			return nil
		})
		done <- err
	}()
	<-entered

	short, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, _, err := s.CreateAdmitted(short, newJob("user:2"), nil, admitUpTo(1)); err != nil {
		t.Fatalf("user:2 blocked behind user:1: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCreateAdmittedReplaysWithoutAskingAdmit(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	idem := &Idempotency{Key: "retry-me", RequestHash: hashOf("body")}

	first, created, err := s.CreateAdmitted(ctx, newJob("user:1"), idem, admitUpTo(1))
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	// The owner is now at its limit; the retry must still get its job back.
	var calls int
	again, created, err := s.CreateAdmitted(ctx, newJob("user:1"), idem, func(ctx context.Context, u []SKUUsage) error {
		calls++
		return admitUpTo(1)(ctx, u)
	})
	if err != nil || created || again.ID != first.ID || calls != 0 {
		t.Fatalf("replay: id=%s created=%v err=%v admit calls=%d", again.ID, created, err, calls)
	}

	_, _, err = s.CreateAdmitted(ctx, newJob("user:1"), &Idempotency{Key: "retry-me", RequestHash: hashOf("other")}, admitUpTo(1))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different body: err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCreateAdmittedTreatsAnExpiredKeyAsUnused(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	idem := &Idempotency{Key: "old", RequestHash: hashOf("body")}

	first := mustCreate(t, s, newJob("user:1"))
	if _, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (owner, key, request_hash, job_id, created_at)
		VALUES ('user:1', 'old', $1, $2, now() - interval '25 hours')`, idem.RequestHash, first.ID); err != nil {
		t.Fatal(err)
	}
	var calls int
	job, created, err := s.CreateAdmitted(ctx, newJob("user:1"), idem, func(context.Context, []SKUUsage) error {
		calls++
		return nil
	})
	if err != nil || !created || job.ID == first.ID || calls != 1 {
		t.Fatalf("expired key: id=%s created=%v err=%v calls=%d", job.ID, created, err, calls)
	}
}
