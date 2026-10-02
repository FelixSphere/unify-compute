package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/events"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

func TestEventsRouteIsNotImplementedWithoutAStore(t *testing.T) {
	s := newTestServer(t, "aws")
	if rec := do(t, s, http.MethodGet, "/compute/v1/jobs/job_1/events", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without key: %d", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/compute/v1/jobs/job_1/events", "sk-abcdefghij", ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("with key, no store: %d", rec.Code)
	}
}

type eventsList struct {
	Object  string          `json:"object"`
	Data    []events.Object `json:"data"`
	HasMore bool            `json:"has_more"`
	Next    string          `json:"next"`
}

// Jobs cannot be submitted through the API yet, so the job and its events
// are written straight to the store.
func TestEventsRouteIsOwnerScopedAndPaginates(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	s := newTestServer(t, "aws")
	s.Events = st
	s.Owner = func(_ context.Context, key string) (string, error) {
		switch key {
		case "sk-owner-a":
			return "owner-a", nil
		case "sk-owner-b":
			return "owner-b", nil
		}
		return "", ErrKeyRejected
	}

	job, _, err := st.Create(ctx, store.NewJob{Owner: "owner-a", SKU: "a10g-24gb-x1", Spec: json.RawMessage(`{}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range []struct {
		t    events.Type
		data any
	}{
		{events.JobQueued, events.QueuedData{SKU: "a10g-24gb-x1"}},
		{events.JobProvisioning, nil},
		{events.JobFailover, events.FailoverData{From: "aws", To: "gcp", Reason: "capacity_exhausted"}},
	} {
		ev, err := events.Append(ctx, st, job.ID, e.t, e.data)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ev.ID)
	}
	path := "/compute/v1/jobs/" + job.ID + "/events"

	rec := do(t, s, http.MethodGet, path, "sk-owner-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	var l eventsList
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if l.Object != "list" || len(l.Data) != 3 || l.HasMore || l.Next != ids[2] {
		t.Fatalf("list %s", rec.Body)
	}
	if l.Data[2].Type != "job.failover" || l.Data[2].JobID != job.ID || l.Data[2].Object != "compute.event" ||
		!strings.Contains(string(l.Data[2].Data), `"to":"gcp"`) {
		t.Errorf("failover event %+v", l.Data[2])
	}

	rec = do(t, s, http.MethodGet, path+"?limit=1&after="+ids[0], "sk-owner-a", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || rec.Code != 200 {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
	if len(l.Data) != 1 || l.Data[0].ID != ids[1] || !l.HasMore || l.Next != ids[1] {
		t.Fatalf("page after %s: %s", ids[0], rec.Body)
	}

	rec = do(t, s, http.MethodGet, path+"?after="+ids[2], "sk-owner-a", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || len(l.Data) != 0 || l.HasMore ||
		!strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("tail: %s", rec.Body)
	}

	for _, c := range []struct {
		name, path, key string
		status          int
		code            string
	}{
		{"another owner", path, "sk-owner-b", 404, "job_not_found"},
		{"missing job", "/compute/v1/jobs/job_00000000000000000000000000/events", "sk-owner-a", 404, "job_not_found"},
		{"foreign cursor", path + "?after=evt_00000000000000000000000000", "sk-owner-a", 400, "invalid_cursor"},
		{"limit zero", path + "?limit=0", "sk-owner-a", 400, "invalid_limit"},
		{"limit over max", path + "?limit=101", "sk-owner-a", 400, "invalid_limit"},
		{"limit not a number", path + "?limit=ten", "sk-owner-a", 400, "invalid_limit"},
		{"rejected key", path, "sk-revoked", 401, "invalid_key"},
	} {
		rec := do(t, s, http.MethodGet, c.path, c.key, "")
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body, c.status, c.code)
		}
	}
}

func TestEventsRouteSaysWhenTheKeyCannotBeChecked(t *testing.T) {
	s := newTestServer(t, "aws")
	s.Events = stubEvents{}
	s.Owner = func(context.Context, string) (string, error) { return "", context.DeadlineExceeded }
	rec := do(t, s, http.MethodGet, "/compute/v1/jobs/job_1/events", "sk-abcdefghij", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"console_unavailable"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

type stubEvents struct{}

func (stubEvents) ListEvents(context.Context, string, string, string, int) (store.EventPage, error) {
	return store.EventPage{}, nil
}
