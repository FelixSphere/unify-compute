package dashboard

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
)

// HistoryFiltered on the shared fake: the kinds filter is recorded for the
// test and the answer is History's.
func (f *fakeLedger) HistoryFiltered(ctx context.Context, accountID int64, hf billing.HistoryFilter) (billing.HistoryPage, error) {
	recordedKinds = hf.Kinds
	return f.History(ctx, accountID, hf.After, hf.Limit)
}

var recordedKinds []string

func TestUsagePeriodFromTo(t *testing.T) {
	f := newFixture(t)
	l := &usageLedger{
		fakeLedger: fakeLedger{usage: []billing.SKUUsage{{SKU: "a10g-24gb-x1", GPUSeconds: 100, Quota: 10_000}}},
		days:       []billing.DayUsage{{Day: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), GPUSeconds: 100, Quota: 10_000}},
	}
	f.h.ledger = l
	s := f.activeUser("ada@example.com")

	rec := f.do(req{method: http.MethodGet, path: "/usage?from=2026-09-28&to=2026-10-02", cookie: s.cookie})
	var u usageView
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("usage %d %s", rec.Code, rec.Body)
	}
	// Inclusive dates in, half-open window to the ledger, one slot per day.
	if !l.from.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) || !l.to.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("window [%v, %v)", l.from, l.to)
	}
	if u.Month != "" || u.From != "2026-09-28" || u.To != "2026-10-02" || len(u.ByDay) != 5 ||
		u.ByDay[1] != (dayUsageView{Date: "2026-09-29", GPUSeconds: 100, USD: "0.0200"}) || u.ByDay[4].Date != "2026-10-02" {
		t.Fatalf("period %q %q %q days %+v", u.Month, u.From, u.To, u.ByDay)
	}
	// A calendar month still reports itself as one.
	rec = f.do(req{method: http.MethodGet, path: "/usage?month=2026-09", cookie: s.cookie})
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || u.Month != "2026-09" || u.From != "2026-09-01" || u.To != "2026-09-30" {
		t.Fatalf("month %q %q %q %v", u.Month, u.From, u.To, err)
	}
	// A single day is a valid range, and exactly 366 days is the ceiling.
	if rec := f.do(req{method: http.MethodGet, path: "/usage?from=2026-10-02&to=2026-10-02", cookie: s.cookie}); rec.Code != 200 {
		t.Fatalf("one day: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(req{method: http.MethodGet, path: "/usage?from=2025-10-03&to=2026-10-03", cookie: s.cookie}); rec.Code != 200 {
		t.Fatalf("366 days: %d %s", rec.Code, rec.Body)
	}
	bad := map[string]string{
		"from=2026-10-01&to=2026-10-02&month=2026-10": "month",
		"from=2026-10-01":               "to",
		"to=2026-10-01":                 "from",
		"from=2026/10/01&to=2026-10-02": "from",
		"from=2026-10-02&to=2026-10-01": "to",
		"from=2025-10-02&to=2026-10-03": "to",
	}
	for q, param := range bad {
		for _, path := range []string{"/usage?", "/usage/jobs?", "/usage/export?"} {
			e := wantErr(t, f.do(req{method: http.MethodGet, path: path + q, cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_period")
			if e.Error.Param != param {
				t.Fatalf("%s%s: param %q, want %q", path, q, e.Error.Param, param)
			}
		}
	}
}

func TestTransactionsKindFilter(t *testing.T) {
	f := newFixture(t)
	f.h.ledger = &fakeLedger{}
	s := f.activeUser("ada@example.com")

	recordedKinds = nil
	if rec := f.do(req{method: http.MethodGet, path: "/transactions?kind=settle&kind=topup", cookie: s.cookie}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(recordedKinds) != 2 || recordedKinds[0] != "settle" || recordedKinds[1] != "topup" {
		t.Fatalf("kinds passed to the ledger: %v", recordedKinds)
	}
	recordedKinds = []string{"stale"}
	if rec := f.do(req{method: http.MethodGet, path: "/transactions", cookie: s.cookie}); rec.Code != 200 || len(recordedKinds) != 0 {
		t.Fatalf("no filter should pass no kinds: %d %v", rec.Code, recordedKinds)
	}
	e := wantErr(t, f.do(req{method: http.MethodGet, path: "/transactions?kind=settle&kind=bonus", cookie: s.cookie}), http.StatusBadRequest, "invalid_request", "invalid_kind")
	if e.Error.Param != "kind" || !strings.Contains(e.Error.Message, "dispute_reversal") {
		t.Fatalf("%+v", e)
	}
}

func TestUsageExportIsFocusShapedCSV(t *testing.T) {
	f := newFixture(t)
	started := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	finished := started.Add(754 * time.Second)
	l := &usageLedger{jobs: billing.JobUsagePage{Rows: []billing.JobUsage{
		{JobID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAV", SKU: "a10g-24gb-x1", Status: "succeeded", KeyID: 7, KeyName: "ci, nightly", KeyDisplay: "uck_AbCd1234",
			StartedAt: &started, FinishedAt: &finished, SettledAt: finished, GPUSeconds: 754, Quota: 125_000},
		{JobID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAW", SKU: "a10g-24gb-x1", Status: "running", KeyID: 7, KeyName: "ci", KeyDisplay: "uck_AbCd1234",
			SettledAt: started.Add(5 * time.Minute), GPUSeconds: 300, Quota: 50_000},
	}}}
	f.h.ledger = l
	s := f.activeUser("ada@example.com")

	rec := f.do(req{method: http.MethodGet, path: "/usage/export?month=2026-10", cookie: s.cookie})
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="unify-compute-usage-2026-10-01-2026-10-31.csv"` {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if l.filter.Limit != exportPage || l.filter.After != "" || l.filter.SKU != "" || l.filter.KeyID != 0 {
		t.Fatalf("export must read unfiltered pages of %d: %+v", exportPage, l.filter)
	}
	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("csv %v rows %d", err, len(records))
	}
	if strings.Join(records[0], ",") != strings.Join(focusColumns, ",") {
		t.Fatalf("header %v", records[0])
	}
	col := func(row []string, name string) string {
		for i, c := range focusColumns {
			if c == name {
				return row[i]
			}
		}
		t.Fatalf("no column %s", name)
		return ""
	}
	r1 := records[1]
	if col(r1, "BillingAccountId") == "" || col(r1, "BillingCurrency") != "USD" ||
		col(r1, "BillingPeriodStart") != "2026-10-01T00:00:00Z" || col(r1, "BillingPeriodEnd") != "2026-11-01T00:00:00Z" ||
		col(r1, "ChargeCategory") != "Usage" || col(r1, "ServiceName") != "Unify Compute" ||
		col(r1, "ResourceId") != "job_01ARZ3NDEKTSV4RRFFQ69G5FAV" || col(r1, "SkuId") != "a10g-24gb-x1" ||
		col(r1, "ChargePeriodStart") != "2026-10-03T09:00:00Z" || col(r1, "ChargePeriodEnd") != "2026-10-03T09:12:34Z" ||
		col(r1, "ConsumedQuantity") != "754" || col(r1, "ConsumedUnit") != "Seconds" ||
		col(r1, "BilledCost") != "0.2500" || col(r1, "EffectiveCost") != "0.2500" ||
		col(r1, "x_ApiKeyId") != "7" || col(r1, "x_ApiKeyName") != "ci, nightly" || col(r1, "x_JobStatus") != "succeeded" {
		t.Fatalf("row 1 %v", r1)
	}
	// No start or finish yet: the last settle stands in for both.
	r2 := records[2]
	if col(r2, "ChargePeriodStart") != "2026-10-03T09:05:00Z" || col(r2, "ChargePeriodEnd") != "2026-10-03T09:05:00Z" || col(r2, "x_JobStatus") != "running" {
		t.Fatalf("row 2 %v", r2)
	}
	wantErr(t, f.do(req{method: http.MethodGet, path: "/usage/export"}), http.StatusUnauthorized, "authentication_error", "session_required")
}
