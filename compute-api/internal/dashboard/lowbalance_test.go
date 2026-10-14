package dashboard

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

func balanceOf(t *testing.T, f *fixture, s session) balanceView {
	t.Helper()
	rec := f.do(req{method: http.MethodGet, path: "/balance", cookie: s.cookie})
	var v balanceView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("balance %d %s", rec.Code, rec.Body)
	}
	return v
}

func TestLowBalanceThreshold(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")
	id := f.accountOf("ada@example.com")

	if got := balanceOf(t, f, s).LowBalanceUSD; got != "5.00" {
		t.Fatalf("default threshold %q, want 5.00", got)
	}

	// Set: the answer is the balance with the new threshold; the account is
	// armed exactly when available is at or above it.
	if _, err := f.db.Exec(f.t.Context(), `UPDATE accounts SET available = 6000000 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	rec := f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, csrf: s.csrf, body: map[string]any{"low_balance_usd": "10.5"}})
	var v balanceView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK || v.LowBalanceUSD != "10.50" || v.AvailableUSD != "12.0000" {
		t.Fatalf("patch %d %s", rec.Code, rec.Body)
	}
	var quota int64
	var armed bool
	read := func() {
		t.Helper()
		if err := f.db.QueryRow(f.t.Context(), `SELECT low_balance_quota, low_balance_armed FROM accounts WHERE id = $1`, id).Scan(&quota, &armed); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if quota != 5_250_000 || !armed {
		t.Fatalf("stored %d armed %v, want 5250000 armed", quota, armed)
	}
	f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, csrf: s.csrf, body: map[string]any{"low_balance_usd": "20"}})
	if read(); quota != 10_000_000 || armed {
		t.Fatalf("over the balance: stored %d armed %v, want disarmed", quota, armed)
	}
	if got := balanceOf(t, f, s).LowBalanceUSD; got != "20.00" {
		t.Fatalf("read back %q", got)
	}

	// 0 turns it off.
	rec = f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, csrf: s.csrf, body: map[string]any{"low_balance_usd": "0"}})
	if rec.Code != http.StatusOK || balanceOf(t, f, s).LowBalanceUSD != "0.00" {
		t.Fatalf("off: %d %s", rec.Code, rec.Body)
	}

	for _, bad := range []any{"-1", "1.234", "abc", "", "10000.01", nil, 5} {
		rec := f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, csrf: s.csrf, body: map[string]any{"low_balance_usd": bad}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: %d %s", bad, rec.Code, rec.Body)
		}
		if bad == 5 {
			continue // a number is a malformed body
		}
		if e := decodeErr(t, rec); e.Error.Code != "invalid_low_balance" || e.Error.Param != "low_balance_usd" {
			t.Fatalf("%v: %+v", bad, e.Error)
		}
	}
	if got := balanceOf(t, f, s).LowBalanceUSD; got != "0.00" {
		t.Fatalf("a refused change changed it to %q", got)
	}

	if n := len(slices.DeleteFunc(f.auditActions(id), func(a string) bool { return a != accounts.ActionLowBalanceChanged })); n != 3 {
		t.Fatalf("%d low_balance_changed audit rows, want 3", n)
	}
}

func TestLowBalanceNeedsSessionAndCSRF(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")
	body := map[string]any{"low_balance_usd": "1"}
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/balance", body: body}), http.StatusUnauthorized, "authentication_error", "session_required")
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, body: body}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPatch, path: "/balance", cookie: s.cookie, csrf: s.csrf, origin: strp("https://evil.example"), body: body}),
		http.StatusForbidden, "permission_error", "csrf_failed")
	if got := balanceOf(t, f, s).LowBalanceUSD; got != "5.00" {
		t.Fatalf("changed to %q without CSRF", got)
	}
}
