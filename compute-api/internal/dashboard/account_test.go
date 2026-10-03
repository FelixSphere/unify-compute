package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ratelimit"
)

// fakeJobs records which job route ran, for which account and job id.
type fakeJobs struct{ calls []string }

func (f *fakeJobs) rec(name string, w http.ResponseWriter, r *http.Request, accountID int64) {
	f.calls = append(f.calls, name+" "+r.PathValue("id")+" "+jsonNum(accountID))
	w.WriteHeader(http.StatusTeapot)
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

func (f *fakeJobs) List(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("list", w, r, a)
}

func (f *fakeJobs) Get(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("get", w, r, a)
}

func (f *fakeJobs) Cancel(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("cancel", w, r, a)
}

func (f *fakeJobs) Logs(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("logs", w, r, a)
}

func (f *fakeJobs) Events(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("events", w, r, a)
}

func (f *fakeJobs) Artifacts(w http.ResponseWriter, r *http.Request, a int64) {
	f.rec("artifacts", w, r, a)
}

type fakeLedger struct {
	usage    []billing.SKUUsage
	days     []billing.DayUsage
	keys     []billing.KeyUsage
	jobs     billing.JobUsagePage
	jobsErr  error
	filter   billing.JobUsageFilter
	from, to time.Time
}

func (f *fakeLedger) UsageByDay(_ context.Context, _ int64, from, to time.Time) ([]billing.DayUsage, error) {
	f.from, f.to = from, to
	return f.days, nil
}

func (f *fakeLedger) UsageByKey(_ context.Context, _ int64, from, to time.Time) ([]billing.KeyUsage, error) {
	f.from, f.to = from, to
	return f.keys, nil
}

func (f *fakeLedger) UsageJobs(_ context.Context, _ int64, from, to time.Time, filter billing.JobUsageFilter) (billing.JobUsagePage, error) {
	f.from, f.to, f.filter = from, to, filter
	return f.jobs, f.jobsErr
}

func (f *fakeLedger) History(context.Context, int64, string, int) (billing.HistoryPage, error) {
	return billing.HistoryPage{}, nil
}

func (f *fakeLedger) Usage(_ context.Context, _ int64, from, to time.Time) ([]billing.SKUUsage, error) {
	f.from, f.to = from, to
	return f.usage, nil
}

var accountRoutes = []struct{ method, path string }{
	{http.MethodGet, "/transactions"},
	{http.MethodGet, "/usage"},
	{http.MethodGet, "/jobs"},
	{http.MethodGet, "/jobs/job_x"},
	{http.MethodPost, "/jobs/job_x/cancel"},
	{http.MethodGet, "/jobs/job_x/events"},
	{http.MethodGet, "/jobs/job_x/logs"},
	{http.MethodGet, "/jobs/job_x/artifacts"},
}

// Without Jobs and Ledger (how every test before A7d builds the handler)
// the routes exist, refuse without a session, and answer 501 with one; the
// balance needs neither and answers from the account row.
func TestAccountRoutesUnwired(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")
	for _, rt := range append(accountRoutes, struct{ method, path string }{http.MethodGet, "/balance"}) {
		wantErr(t, f.do(req{method: rt.method, path: rt.path}), http.StatusUnauthorized, "authentication_error", "session_required")
	}
	for _, rt := range accountRoutes {
		wantErr(t, f.do(req{method: rt.method, path: rt.path, cookie: s.cookie, csrf: s.csrf}), http.StatusNotImplemented, "not_implemented", "not_implemented")
	}
	rec := f.do(req{method: http.MethodGet, path: "/balance", cookie: s.cookie})
	var b map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("balance %d %s", rec.Code, rec.Body)
	}
	limits, _ := b["limits"].(map[string]any)
	if b["available_usd"] != "0.0000" || b["held_usd"] != "0.0000" || b["currency"] != "USD" ||
		limits["topup_min_usd"] != "10.00" || limits["topup_max_usd"] != "100.00" {
		t.Fatalf("balance %s", rec.Body)
	}
}

// The job routes reach the job API's handlers with the session's account
// and the path's job id; cancel is a non-GET, so it needs the CSRF header
// and a same-origin Origin before anything runs.
func TestAccountJobRoutesDelegate(t *testing.T) {
	f := newFixture(t)
	jobs := &fakeJobs{}
	f.h.jobs = jobs
	s := f.activeUser("ada@example.com")
	id := jsonNum(f.accountOf("ada@example.com"))

	wantErr(t, f.do(req{method: http.MethodPost, path: "/jobs/job_x/cancel", cookie: s.cookie}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/jobs/job_x/cancel", cookie: s.cookie, csrf: s.csrf, origin: strp("https://evil.example")}),
		http.StatusForbidden, "permission_error", "csrf_failed")
	if len(jobs.calls) != 0 {
		t.Fatalf("refused cancels reached the job API: %v", jobs.calls)
	}
	for _, rt := range accountRoutes[2:] {
		if rec := f.do(req{method: rt.method, path: rt.path, cookie: s.cookie, csrf: s.csrf}); rec.Code != http.StatusTeapot {
			t.Fatalf("%s %s: %d %s", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
	want := []string{"list  " + id, "get job_x " + id, "cancel job_x " + id, "events job_x " + id, "logs job_x " + id, "artifacts job_x " + id}
	if strings.Join(jobs.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %q, want %q", jobs.calls, want)
	}
}

// Reads are limited per account; another account keeps its own budget, and
// cancel is not a read.
func TestAccountReadLimit(t *testing.T) {
	f := newFixture(t)
	f.h.jobs = &fakeJobs{}
	f.h.reads = ratelimit.New(1, time.Hour)
	ada := f.activeUser("ada@example.com")
	bob := f.activeUser("bob@example.com")
	if rec := f.do(req{method: http.MethodGet, path: "/balance", cookie: ada.cookie}); rec.Code != http.StatusOK {
		t.Fatalf("first read %d %s", rec.Code, rec.Body)
	}
	rec := f.do(req{method: http.MethodGet, path: "/jobs", cookie: ada.cookie})
	wantErr(t, rec, http.StatusTooManyRequests, "rate_limited", "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	if rec := f.do(req{method: http.MethodPost, path: "/jobs/job_x/cancel", cookie: ada.cookie, csrf: ada.csrf}); rec.Code != http.StatusTeapot {
		t.Fatalf("cancel counted as a read: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(req{method: http.MethodGet, path: "/balance", cookie: bob.cookie}); rec.Code != http.StatusOK {
		t.Fatalf("bob shares ada's budget: %d %s", rec.Code, rec.Body)
	}
}

func TestUsageMonthAndMeters(t *testing.T) {
	f := newFixture(t)
	l := &fakeLedger{usage: []billing.SKUUsage{
		{SKU: "a10g-24gb-x1", GPUSeconds: 754, Quota: 125_000},
		{SKU: "egress", Quota: 1},
		{SKU: "h100-80gb-x1", GPUSeconds: 10, Quota: 7_000},
		{SKU: "storage", Quota: 5_000},
	}, days: []billing.DayUsage{
		{Day: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), GPUSeconds: 754, Quota: 125_000},
	}, keys: []billing.KeyUsage{
		{KeyID: 7, Name: "ci", Display: "uck_AbCd1234", Jobs: 2, GPUSeconds: 700, Quota: 100_000},
		{KeyID: 3, Jobs: 1, GPUSeconds: 64, Quota: 32_000}, // key row deleted: name and display empty
	}}
	f.h.ledger = l
	s := f.activeUser("ada@example.com")

	rec := f.do(req{method: http.MethodGet, path: "/usage", cookie: s.cookie})
	var u usageView
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("usage %d %s", rec.Code, rec.Body)
	}
	if u.Month != "2026-10" || !l.from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !l.to.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("default month %q [%v, %v)", u.Month, l.from, l.to)
	}
	total := int64(125_000 + 1 + 7_000 + 5_000)
	if u.GPUUSD != billing.USD(132_000) || u.StorageUSD != "0.0100" || u.EgressUSD != billing.USD(1) ||
		u.TotalUSD != billing.USD(total) || u.GPUSeconds != 764 || len(u.BySKU) != 2 ||
		u.BySKU[0] != (skuUsageView{SKU: "a10g-24gb-x1", GPUSeconds: 754, USD: "0.2500"}) {
		t.Fatalf("usage %+v", u)
	}
	// Every day of October has a slot; only the 3rd has a settle.
	if len(u.ByDay) != 31 || u.ByDay[0].Date != "2026-10-01" || u.ByDay[30].Date != "2026-10-31" ||
		u.ByDay[2] != (dayUsageView{Date: "2026-10-03", GPUSeconds: 754, USD: "0.2500"}) ||
		u.ByDay[3] != (dayUsageView{Date: "2026-10-04", GPUSeconds: 0, USD: "0.0000"}) {
		t.Fatalf("by_day %+v", u.ByDay)
	}
	if u.Jobs != 3 || len(u.ByKey) != 2 ||
		u.ByKey[0] != (keyUsageView{Key: keyRef{ID: 7, Name: "ci", Display: "uck_AbCd1234"}, Jobs: 2, GPUSeconds: 700, USD: "0.2000"}) ||
		u.ByKey[1].Key.Name != "" {
		t.Fatalf("by_key %+v jobs %d", u.ByKey, u.Jobs)
	}

	f.do(req{method: http.MethodGet, path: "/usage?month=2025-12", cookie: s.cookie})
	if !l.from.Equal(time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)) || !l.to.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("December runs [%v, %v)", l.from, l.to)
	}
	for _, bad := range []string{"2026-13", "2026-1", "202610", "october"} {
		e := wantErr(t, f.do(req{method: http.MethodGet, path: "/usage?month=" + bad, cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_month")
		if e.Error.Param != "month" {
			t.Fatalf("%s: param %q", bad, e.Error.Param)
		}
	}
	for _, bad := range []string{"0", "101", "x"} {
		wantErr(t, f.do(req{method: http.MethodGet, path: "/transactions?limit=" + bad, cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_limit")
	}
}

func TestViewEntry(t *testing.T) {
	at := time.Unix(1791000000, 0)
	credit := viewEntry(billing.HistoryEntry{ID: "be_1", Kind: "admin_credit", AvailableDelta: 2_500_000, AvailableAfter: 2_500_000,
		RefType: "admin", RefID: "ae_01J8", CreatedAt: at})
	if credit.Ref != nil || credit.JobID != nil || credit.AmountUSD != "5.0000" || len(credit.Detail) != 0 {
		t.Fatalf("credit %+v", credit)
	}
	settle := viewEntry(billing.HistoryEntry{ID: "be_2", Kind: "settle", HeldDelta: -125_000, AvailableAfter: 1_900_000, HeldAfter: 475_000,
		RefType: "hold", RefID: "hold_x", JobID: "job_y", SKU: "a10g-24gb-x1", CreatedAt: at})
	if settle.AmountUSD != "-0.2500" || settle.AvailableDeltaUSD != "0.0000" || settle.HeldAfterUSD != "0.9500" ||
		*settle.JobID != "job_y" || *settle.Ref != (entryRef{Type: "job", ID: "job_y"}) || settle.Detail["sku"] != "a10g-24gb-x1" {
		t.Fatalf("settle %+v", settle)
	}
	raw, _ := json.Marshal(settle)
	if strings.Contains(string(raw), "hold_x") {
		t.Fatalf("hold id exposed: %s", raw)
	}
	topup := viewEntry(billing.HistoryEntry{ID: "be_3", Kind: "topup", AvailableDelta: 5_000_000, RefType: "topup", RefID: "tu_1", CreatedAt: at})
	if topup.JobID != nil || *topup.Ref != (entryRef{Type: "topup", ID: "tu_1"}) {
		t.Fatalf("topup %+v", topup)
	}
}

func TestUsageJobsListsChargedJobs(t *testing.T) {
	f := newFixture(t)
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	finished := started.Add(754 * time.Second)
	l := &fakeLedger{jobs: billing.JobUsagePage{HasMore: true, Rows: []billing.JobUsage{
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
