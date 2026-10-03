package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSetPlacementRecordsWhereTheJobLanded(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("user:1"))
	if j.Placement != nil {
		t.Fatalf("new job has placement %s, want null", j.Placement)
	}
	if _, err := s.UpdateStatus(ctx, j.ID, StatusProvisioning, nil); err != nil {
		t.Fatal(err)
	}
	p := Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"}
	got, err := s.SetPlacement(ctx, j.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	var back Placement
	if err := json.Unmarshal(got.Placement, &back); err != nil || back != p {
		t.Fatalf("placement = %s (%v), want %+v", got.Placement, err, p)
	}
	if got.Status != StatusProvisioning {
		t.Fatalf("SetPlacement changed status to %s", got.Status)
	}
	reread, err := s.Get(ctx, "user:1", j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(reread.Placement) != string(got.Placement) {
		t.Fatalf("persisted %s, returned %s", reread.Placement, got.Placement)
	}
}

func TestSetPlacementReplacesForARetriedAttempt(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("user:1"))
	if _, err := s.UpdateStatus(ctx, j.ID, StatusProvisioning, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlacement(ctx, j.ID, Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStatus(ctx, j.ID, StatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	second := Placement{Cloud: "lambda", Region: "us-east-1", InstanceType: "gpu_1x_a10"}
	got, err := s.SetPlacement(ctx, j.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	var back Placement
	_ = json.Unmarshal(got.Placement, &back)
	if back != second {
		t.Fatalf("placement = %+v, want %+v", back, second)
	}
}

func TestSetPlacementRefusesTerminalJob(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	j := mustCreate(t, s, newJob("user:1"))
	if _, err := s.UpdateStatus(ctx, j.ID, StatusCancelled, nil); err != nil {
		t.Fatal(err)
	}
	_, err := s.SetPlacement(ctx, j.ID, Placement{Cloud: "aws", Region: "us-east-1", InstanceType: "g5.xlarge"})
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("err = %v, want ErrTerminal", err)
	}
	reread, _ := s.Get(ctx, "user:1", j.ID)
	if reread.Placement != nil {
		t.Fatalf("terminal job gained placement %s", reread.Placement)
	}
}

func TestSetPlacementUnknownJob(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_, err := s.SetPlacement(context.Background(), "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", Placement{Cloud: "aws"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
