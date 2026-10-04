package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

// KeySpend on the shared fake: nothing spent, so spent_this_month_usd reads
// 0.0000 whenever a ledger is wired.
func (f *fakeLedger) KeySpend(context.Context, int64, time.Time) (map[int64]int64, error) {
	return map[int64]int64{}, nil
}

type spendLedger struct {
	fakeLedger
	spend map[int64]int64
	from  time.Time
}

func (f *spendLedger) KeySpend(_ context.Context, _ int64, from time.Time) (map[int64]int64, error) {
	f.from = from
	return f.spend, nil
}

func TestKeyMonthlyCap(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")

	// Created with a cap: the view shows it in dollars, and the cap value is
	// what the ledger would be checked against.
	rec := f.do(req{method: http.MethodPost, path: "/keys", cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"name": "nightly", "scopes": []string{"jobs"}, "monthly_cap_usd": "12.50"}})
	var created createdKeyView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if created.MonthlyCapUSD == nil || *created.MonthlyCapUSD != "12.5000" || created.SpentThisMonthUSD != nil {
		t.Fatalf("created view %+v (no ledger wired: spent must be null)", created.keyView)
	}
	// Without a cap: null.
	rec = f.do(req{method: http.MethodPost, path: "/keys", cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"name": "open", "scopes": []string{"read"}}})
	var open createdKeyView
	if err := json.Unmarshal(rec.Body.Bytes(), &open); err != nil || rec.Code != http.StatusCreated || open.MonthlyCapUSD != nil {
		t.Fatalf("uncapped %d %s", rec.Code, rec.Body)
	}

	// Bad caps are refused before anything is created.
	for _, bad := range []string{"0", "-5", "12.345", "abc", "1000000.01", ""} {
		e := wantErr(t, f.do(req{method: http.MethodPost, path: "/keys", cookie: s.cookie, csrf: s.csrf,
			body: map[string]any{"name": "x", "scopes": []string{"jobs"}, "monthly_cap_usd": bad}}), http.StatusBadRequest, "invalid_request", "invalid_cap")
		if e.Error.Param != "monthly_cap_usd" {
			t.Fatalf("%q: param %q", bad, e.Error.Param)
		}
	}

	// PATCH sets, changes and clears; the list shows spend from the ledger.
	f.h.ledger = &spendLedger{spend: map[int64]int64{created.keyView.ID: 2_500_000}}
	rec = f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(created.keyView.ID), cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": "20"}})
	var patched keyView
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("patch %d %s", rec.Code, rec.Body)
	}
	if patched.MonthlyCapUSD == nil || *patched.MonthlyCapUSD != "20.0000" || patched.SpentThisMonthUSD == nil || *patched.SpentThisMonthUSD != "5.0000" {
		t.Fatalf("patched %+v", patched)
	}
	if got := f.h.ledger.(*spendLedger).from; !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("spend window starts %v, want the UTC month", got)
	}
	rec = f.do(req{method: http.MethodGet, path: "/keys", cookie: s.cookie})
	var list []keyView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list) != 2 {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	for _, v := range list {
		if v.SpentThisMonthUSD == nil {
			t.Fatalf("every key gets a spend figure when a ledger is wired: %+v", v)
		}
		if v.ID == open.keyView.ID && *v.SpentThisMonthUSD != "0.0000" {
			t.Fatalf("a key with no holds spent nothing: %+v", v)
		}
	}
	rec = f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(created.keyView.ID), cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": nil}})
	if err := json.Unmarshal(rec.Body.Bytes(), &patched); err != nil || rec.Code != http.StatusOK || patched.MonthlyCapUSD != nil {
		t.Fatalf("clear %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(created.keyView.ID), cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": "0"}}), http.StatusBadRequest, "invalid_request", "invalid_cap")

	// A revoked key, a missing one, and another account's are all 404.
	if rec := f.do(req{method: http.MethodDelete, path: "/keys/" + itoa(open.keyView.ID), cookie: s.cookie, csrf: s.csrf}); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(open.keyView.ID), cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": "5"}}), http.StatusNotFound, "not_found", "not_found")
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/keys/999999", cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": "5"}}), http.StatusNotFound, "not_found", "not_found")
	other := f.activeUser("bob@example.com")
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(created.keyView.ID), cookie: other.cookie, csrf: other.csrf,
		body: map[string]any{"monthly_cap_usd": "5"}}), http.StatusNotFound, "not_found", "not_found")

	// Reauth: a cap is a spending control.
	f.clock.advance(11 * time.Minute)
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/keys/" + itoa(created.keyView.ID), cookie: s.cookie, csrf: s.csrf,
		body: map[string]any{"monthly_cap_usd": "5"}}), http.StatusUnauthorized, "authentication_error", "reauth_required")

	// The audit trail names the change.
	var n int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = $1`, accounts.ActionKeyCapChanged).Scan(&n); err != nil || n < 2 {
		t.Fatalf("audit rows %d %v", n, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
