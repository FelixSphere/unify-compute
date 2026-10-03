package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"
)

var eventIDPattern = regexp.MustCompile(`^evt_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

func appendEvent(t *testing.T, s *Store, jobID, typ, data string) Event {
	t.Helper()
	e, err := s.AppendEvent(context.Background(), NewEvent{JobID: jobID, Type: typ, Data: json.RawMessage(data)})
	if err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
	return e
}

func TestAppendEventAndListInOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("owner-a"))

	types := []string{"job.queued", "job.provisioning", "job.failover", "job.placed", "job.running", "job.succeeded"}
	var ids []string
	for _, typ := range types {
		e := appendEvent(t, s, j.ID, typ, `{}`)
		if !eventIDPattern.MatchString(e.ID) {
			t.Fatalf("event id %q", e.ID)
		}
		ids = append(ids, e.ID)
	}

	page, err := s.ListEvents(ctx, "owner-a", j.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore || len(page.Events) != len(types) {
		t.Fatalf("got %d events has_more=%v", len(page.Events), page.HasMore)
	}
	for i, e := range page.Events {
		if e.ID != ids[i] || e.Type != types[i] {
			t.Errorf("event %d = %s %s, want %s %s", i, e.ID, e.Type, ids[i], types[i])
		}
	}

	page, err = s.ListEvents(ctx, "owner-a", j.ID, ids[1], 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Events) != 2 || page.Events[0].ID != ids[2] || page.Events[1].ID != ids[3] {
		t.Fatalf("page after %s: %+v", ids[1], page)
	}
	page, err = s.ListEvents(ctx, "owner-a", j.ID, ids[len(ids)-1], 0)
	if err != nil || len(page.Events) != 0 || page.HasMore {
		t.Fatalf("page after the tail: %+v %v", page, err)
	}
}

func TestListEventsIsOwnerScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustCreate(t, s, newJob("owner-a"))
	b := mustCreate(t, s, newJob("owner-b"))
	appendEvent(t, s, a.ID, "job.queued", `{"sku":"a10g-24gb-x1"}`)
	other := appendEvent(t, s, b.ID, "job.queued", `{"sku":"a10g-24gb-x1"}`)

	if _, err := s.ListEvents(ctx, "owner-b", a.ID, "", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("another owner's job: %v, want ErrNotFound", err)
	}
	if _, err := s.ListEvents(ctx, "owner-a", "job_00000000000000000000000000", "", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job: %v, want ErrNotFound", err)
	}
	if _, err := s.ListEvents(ctx, "owner-a", a.ID, other.ID, 0); !errors.Is(err, ErrInvalidEventCursor) {
		t.Errorf("cursor from another job: %v, want ErrInvalidEventCursor", err)
	}
}

func TestAppendEventToMissingJob(t *testing.T) {
	s := newTestStore(t)
	_, err := s.AppendEvent(context.Background(), NewEvent{JobID: "job_00000000000000000000000000", Type: "job.queued"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("owner-a"))
	e := appendEvent(t, s, j.ID, "job.failed", `{"error":{"code":"image_pull_failed"},"retryable":false}`)
	if _, err := s.pool.Exec(ctx, `UPDATE job_events SET data = '{}' WHERE id = $1`, e.ID); err == nil {
		t.Fatal("UPDATE on job_events succeeded; the log must be append-only")
	}
	page, err := s.ListEvents(ctx, "owner-a", j.ID, "", 0)
	if err != nil || len(page.Events) != 1 || string(page.Events[0].Data) == "{}" {
		t.Fatalf("event was rewritten: %+v %v", page, err)
	}
}

// Fan-out happens in the append statement: which webhooks get a delivery is
// decided by owner, the job's key, and the webhook's event types.
func TestAppendEventQueuesMatchingWebhooks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	key1, key2 := int64(1), int64(2)
	nj := newJob("owner-a")
	nj.TokenID = &key1
	j := mustCreate(t, s, nj)

	hook := func(owner string, token *int64, types ...string) Webhook {
		w, err := s.CreateWebhook(ctx, NewWebhook{Owner: owner, TokenID: token, URL: "https://hooks.example/x",
			Secret: "whsec_0123456789abcdef0123456789abcdef", EventTypes: types})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	sameKey := hook("owner-a", &key1, "job.succeeded")
	ownerWide := hook("owner-a", nil, "job.succeeded", "job.failed")
	hook("owner-a", &key2, "job.succeeded")            // another key of the same owner
	hook("owner-b", nil, "job.succeeded")              // another owner
	hook("owner-a", &key1, "job.queued", "job.placed") // does not list the type

	e := appendEvent(t, s, j.ID, "job.succeeded", `{}`)
	ds, err := s.DeliveriesOf(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range ds {
		got[d.WebhookID] = true
		if d.Status != "pending" || d.Attempts != 0 || !d.NextAttemptAt.Equal(e.CreatedAt) {
			t.Errorf("new delivery %+v", d)
		}
	}
	if len(got) != 2 || !got[sameKey.ID] || !got[ownerWide.ID] {
		t.Fatalf("deliveries to %v, want exactly %s and %s", got, sameKey.ID, ownerWide.ID)
	}

	running := appendEvent(t, s, j.ID, "job.running", `{}`)
	if ds, _ := s.DeliveriesOf(ctx, running.ID); len(ds) != 0 {
		t.Fatalf("job.running queued %d deliveries; no webhook subscribed to it", len(ds))
	}
}

func TestClaimDoesNotOverlapAndHonoursLease(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("owner-a"))
	if _, err := s.CreateWebhook(ctx, NewWebhook{Owner: "owner-a", URL: "https://hooks.example/x",
		Secret: "whsec_0123456789abcdef0123456789abcdef", EventTypes: []string{"job.succeeded"}}); err != nil {
		t.Fatal(err)
	}
	appendEvent(t, s, j.ID, "job.succeeded", `{}`)
	now := time.Now().Add(time.Minute)

	first, err := s.ClaimDeliveries(ctx, now, time.Minute, 10)
	if err != nil || len(first) != 1 || first[0].Attempt != 1 {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	if again, _ := s.ClaimDeliveries(ctx, now.Add(59*time.Second), time.Minute, 10); len(again) != 0 {
		t.Fatalf("claimed %d inside the lease", len(again))
	}
	// The first worker never reported: the lease runs out and the delivery is
	// attempted again, counted as attempt 2.
	second, err := s.ClaimDeliveries(ctx, now.Add(time.Minute), time.Minute, 10)
	if err != nil || len(second) != 1 || second[0].Attempt != 2 {
		t.Fatalf("claim after lease: %+v %v", second, err)
	}
	// The overtaken worker's late result must not settle the delivery.
	if err := s.MarkDelivered(ctx, first[0].ID, first[0].Attempt, now, 200); err != nil {
		t.Fatal(err)
	}
	ds, _ := s.DeliveriesOf(ctx, first[0].Event.ID)
	if ds[0].Status != "pending" {
		t.Fatalf("stale attempt settled the delivery: %+v", ds[0])
	}
}
