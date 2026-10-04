package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
)

// The usage views the ledger gained after account_test.go's fakeLedger was
// written. Zero answers here keep that fake a Ledger; usageLedger below is
// the fake these tests drive.
func (f *fakeLedger) UsageByDay(context.Context, int64, time.Time, time.Time) ([]billing.DayUsage, error) {
	return nil, nil
}

func (f *fakeLedger) UsageByKey(context.Context, int64, time.Time, time.Time) ([]billing.KeyUsage, error) {
	return nil, nil
}

func (f *fakeLedger) UsageJobs(context.Context, int64, time.Time, time.Time, billing.JobUsageFilter) (billing.JobUsagePage, error) {
	return billing.JobUsagePage{}, nil
}

type usageLedger struct {
	fakeLedger
	days    []billing.DayUsage
	keys    []billing.KeyUsage
	jobs    billing.JobUsagePage
	jobsErr error
	filter  billing.JobUsageFilter
}

func (f *usageLedger) UsageByDay(_ context.Context, _ int64, from, to time.Time) ([]billing.DayUsage, error) {
	f.from, f.to = from, to
	return f.days, nil
}

func (f *usageLedger) UsageByKey(_ context.Context, _ int64, from, to time.Time) ([]billing.KeyUsage, error) {
	f.from, f.to = from, to
	return f.keys, nil
}

func (f *usageLedger) UsageJobs(_ context.Context, _ int64, from, to time.Time, filter billing.JobUsageFilter) (billing.JobUsagePage, error) {
	f.from, f.to, f.filter = from, to, filter
	return f.jobs, f.jobsErr
}

func TestUsageByDayAndByKey(t *testing.T) {
	f := newFixture(t)
	l := &usageLedger{
		fakeLedger: fakeLedger{usage: []billing.SKUUsage{{SKU: "a10g-24gb-x1", GPUSeconds: 754, Quota: 125_000}}},
		days:       []billing.DayUsage{{Day: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), GPUSeconds: 754, Quota: 125_000}},
		keys: []billing.KeyUsage{
			{KeyID: 7, Name: "ci", Display: "uck_AbCd1234", Jobs: 2, GPUSeconds: 700, Quota: 100_000},
			{KeyID: 3, Jobs: 1, GPUSeconds: 54, Quota: 25_000}, // key row deleted: name and display empty
		},
	}
	f.h.ledger = l
	s := f.activeUser("ada@example.com")

	rec := f.do(req{method: http.MethodGet, path: "/usage?month=2026-10", cookie: s.cookie})
	var u usageView
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("usage %d %s", rec.Code, rec.Body)
	}
	// Every day of October has a slot; only the 3rd has a settle.
	if len(u.ByDay) != 31 || u.ByDay[0].Date != "2026-10-01" || u.ByDay[30].Date != "2026-10-31" ||
		u.ByDay[2] != (dayUsageView{Date: "2026-10-03", GPUSeconds: 754, USD: "0.2500"}) ||
		u.ByDay[3] != (dayUsageView{Date: "2026-10-04", GPUSeconds: 0, USD: "0.0000"}) {
		t.Fatalf("by_day %+v", u.ByDay)
	}
	if u.Jobs != 3 || len(u.ByKey) != 2 ||
		u.ByKey[0] != (keyUsageView{Key: keyRef{ID: 7, Name: "ci", Display: "uck_AbCd1234"}, Jobs: 2, GPUSeconds: 700, USD: "0.2000"}) ||
		u.ByKey[1] != (keyUsageView{Key: keyRef{ID: 3}, Jobs: 1, GPUSeconds: 54, USD: "0.0500"}) {
		t.Fatalf("by_key %+v jobs %d", u.ByKey, u.Jobs)
	}
	// February has 28 slots, and a month with no settles still has them all.
	rec = f.do(req{method: http.MethodGet, path: "/usage?month=2026-02", cookie: s.cookie})
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || len(u.ByDay) != 28 || u.ByDay[27].Date != "2026-02-28" {
		t.Fatalf("february %d days: %v", len(u.ByDay), err)
	}
}

func TestUsageJobsListsChargedJobs(t *testing.T) {
	f := newFixture(t)
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	finished := started.Add(754 * time.Second)
	l := &usageLedger{jobs: billing.JobUsagePage{HasMore: true, Rows: []billing.JobUsage{
		{JobID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAV", SKU: "a10g-24gb-x1", Status: "succeeded", KeyID: 7, KeyName: "ci", KeyDisplay: "uck_AbCd1234",
			StartedAt: &started, FinishedAt: &finished, SettledAt: finished, GPUSeconds: 754, Quota: 125_000},
		{JobID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAW", SKU: "a10g-24gb-x1", Status: "running", KeyID: 7, KeyName: "ci", KeyDisplay: "uck_AbCd1234",
			StartedAt: &started, SettledAt: started.Add(5 * time.Minute), GPUSeconds: 300, Quota: 50_000},
	}}}
	f.h.ledger = l
	s := f.activeUser("ada@example.com")

	rec := f.do(req{method: http.MethodGet, path: "/usage/jobs?sku=a10g-24gb-x1&key=7&limit=2&after=job_01ARZ3NDEKTSV4RRFFQ69G5FAU", cookie: s.cookie})
	var body struct {
		Object  string         `json:"object"`
		Data    []jobUsageView `json:"data"`
		HasMore bool           `json:"has_more"`
		Next    string         `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("usage/jobs %d %s", rec.Code, rec.Body)
	}
	if l.filter != (billing.JobUsageFilter{SKU: "a10g-24gb-x1", KeyID: 7, After: "job_01ARZ3NDEKTSV4RRFFQ69G5FAU", Limit: 2}) {
		t.Fatalf("filter %+v", l.filter)
	}
	if !l.from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !l.to.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("default month [%v, %v)", l.from, l.to)
	}
	if body.Object != "list" || !body.HasMore || body.Next != "job_01ARZ3NDEKTSV4RRFFQ69G5FAW" || len(body.Data) != 2 {
		t.Fatalf("list %+v", body)
	}
	first := body.Data[0]
	if first.Object != "compute.job_usage" || first.Status != "succeeded" || first.Key != (keyRef{ID: 7, Name: "ci", Display: "uck_AbCd1234"}) ||
		first.StartedAt == nil || *first.StartedAt != started.Unix() || first.FinishedAt == nil || first.SettledAt != finished.Unix() ||
		first.GPUSeconds != 754 || first.USD != "0.2500" {
		t.Fatalf("row %+v", first)
	}
	if body.Data[1].FinishedAt != nil {
		t.Fatalf("a running job has no finished_at: %+v", body.Data[1])
	}

	for _, bad := range []string{"key=0", "key=x", "key=-1"} {
		e := wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/jobs?" + bad, cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_key")
		if e.Error.Param != "key" {
			t.Fatalf("%s: param %q", bad, e.Error.Param)
		}
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/jobs?limit=101", cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_limit")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/jobs?month=2026-13", cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_month")
	l.jobsErr = billing.ErrInvalidCursor
	e := wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/jobs?after=job_nope", cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_cursor")
	if e.Error.Param != "after" {
		t.Fatalf("param %q", e.Error.Param)
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/jobs"}), http.StatusUnauthorized, "authentication_error", "session_required")
}
