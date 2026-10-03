package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A status change and the event announcing it commit together or not at
// all; a refused transition leaves no event behind.
func TestUpdateStatusWithEventIsOneTransaction(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	id := NewJobID(time.Now())
	job, _, err := s.Create(ctx, NewJob{ID: id, Owner: "user:1", SKU: "a10g-24gb-x1", Spec: json.RawMessage(`{}`),
		Events: []NewEvent{{Type: "job.queued", Data: json.RawMessage(`{"sku":"a10g-24gb-x1"}`)}}}, nil)
	if err != nil || job.ID != id {
		t.Fatalf("create with a chosen id: %v %s", err, job.ID)
	}
	ev := func(typ string) func(Job) (*NewEvent, error) {
		return func(Job) (*NewEvent, error) { return &NewEvent{Type: typ}, nil }
	}
	if _, err := s.UpdateStatusWithEvent(ctx, id, StatusProvisioning, nil, ev("job.provisioning")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStatusWithEvent(ctx, id, StatusSucceeded, nil, ev("job.succeeded")); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("provisioning -> succeeded: %v", err)
	}
	boom := errors.New("event refused")
	if _, err := s.UpdateStatusWithEvent(ctx, id, StatusCancelled, nil, func(Job) (*NewEvent, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("event error: %v", err)
	}
	got, err := s.Get(ctx, "user:1", id)
	if err != nil || got.Status != StatusProvisioning {
		t.Fatalf("a failed event must roll the status back: %+v %v", got, err)
	}
	if _, err := s.SetPlacementWithEvent(ctx, id, Placement{Cloud: "aws"}, ev("job.placed")); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListEvents(ctx, "user:1", id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range page.Events {
		types = append(types, e.Type)
	}
	if len(types) != 3 || types[0] != "job.queued" || types[1] != "job.provisioning" || types[2] != "job.placed" {
		t.Fatalf("events %v", types)
	}
}
