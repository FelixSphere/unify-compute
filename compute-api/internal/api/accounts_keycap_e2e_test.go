package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A key's monthly cap, end to end on the local biller: the cap is checked
// when the hold is placed, counts open holds, and is the key's alone. Sorts
// before e2e_test.go so TestEndToEnd sees key_cap_exceeded produced.
func TestKeyMonthlyCapEndToEnd(t *testing.T) {
	if os.Getenv("COMPUTE_TEST_DATABASE_URL") == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping the end-to-end test")
	}
	a := newAccountsE2E(t)
	s, accountID := a.signupAndLogin("cap@example.com")
	id := strconv.FormatInt(accountID, 10)
	a.admin("account", "enable-compute", id, "--reason", "e2e: key cap")
	a.admin("balance", "credit", id, "--usd", "5", "--reason", "e2e: key cap")
	credit := a.balance(accountID).Available

	type keyResp struct {
		ID            int64   `json:"id"`
		Secret        string  `json:"secret"`
		MonthlyCapUSD *string `json:"monthly_cap_usd"`
	}
	// The login just now counts as the reauth key creation needs.
	rec := a.dashCall(http.MethodPost, "/keys", s, map[string]any{"name": "capped", "scopes": []string{"jobs"}, "monthly_cap_usd": "1.00"})
	a.wantStatus("create capped key", rec, http.StatusCreated)
	var k keyResp
	if err := json.Unmarshal(rec.Body.Bytes(), &k); err != nil || k.MonthlyCapUSD == nil || *k.MonthlyCapUSD != "1.0000" {
		t.Fatalf("created key %s %v", rec.Body, err)
	}

	// The template holds 3600 s of a10g at USD 1.20/h = USD 1.20, over the
	// cap: refused before the hold, so nothing moves and nothing launches.
	r, _ := a.submit(k.Secret, templateBody(""))
	if r.code != http.StatusForbidden || r.errCode() != "key_cap_exceeded" {
		t.Fatalf("submit over the cap: %d %s", r.code, r.body)
	}
	if body := string(r.body); !strings.Contains(body, "USD 1.0000") || !strings.Contains(body, "USD 1.2000") || !strings.Contains(body, "USD 0.0000 is spent") {
		t.Fatalf("message should name cap, hold and spend: %s", body)
	}
	// Only the starting credit is on the ledger: no hold, no release.
	if b := a.balance(accountID); b.Available != credit || b.Held != 0 || strings.Join(a.entryKinds(accountID), ",") != "admin_credit" {
		t.Fatalf("balance moved on a refused submit: %+v %v", b, a.entryKinds(accountID))
	}
	if launches := a.sky.Launches(); len(launches) != 0 {
		t.Fatalf("launched %+v", launches)
	}

	// Raised to USD 2.00: the first job fits and its open hold now counts
	// against the cap, so a second identical job does not.
	rec = a.dashCall(http.MethodPatch, "/keys/"+strconv.FormatInt(k.ID, 10), s, map[string]any{"monthly_cap_usd": "2.00"})
	a.wantStatus("raise cap", rec, http.StatusOK)
	r, _ = a.submit(k.Secret, templateBody(""))
	if r.code != http.StatusCreated {
		t.Fatalf("submit under the raised cap: %d %s", r.code, r.body)
	}
	if b := a.balance(accountID); b.Held != quotaOf(3600) {
		t.Fatalf("held after the first job %+v", b)
	}
	r, _ = a.submit(k.Secret, templateBody(""))
	if r.code != http.StatusForbidden || r.errCode() != "key_cap_exceeded" || !strings.Contains(string(r.body), "USD 1.2000 is spent or held") {
		t.Fatalf("second job must count the open hold: %d %s", r.code, r.body)
	}

	// The cap is the key's alone: an uncapped key on the same account runs.
	rec = a.dashCall(http.MethodPost, "/keys", s, map[string]any{"name": "open", "scopes": []string{"jobs"}})
	a.wantStatus("create uncapped key", rec, http.StatusCreated)
	var open keyResp
	if err := json.Unmarshal(rec.Body.Bytes(), &open); err != nil || open.MonthlyCapUSD != nil {
		t.Fatalf("uncapped key %s %v", rec.Body, err)
	}
	if r, _ = a.submit(open.Secret, templateBody("")); r.code != http.StatusCreated {
		t.Fatalf("uncapped key's job: %d %s", r.code, r.body)
	}

	// What the cap is compared with: the capped key's open hold counts, the
	// uncapped key's does too, and nothing else does. This harness mounts
	// the dashboard without a ledger, so its key list carries no spend
	// figure; the ledger read the dashboard would use is checked directly.
	spend, err := a.bill.KeySpend(a.ctx, accountID, monthStartOf(a.clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if spend[k.ID] != quotaOf(3600) || spend[open.ID] != quotaOf(3600) || len(spend) != 2 {
		t.Fatalf("key spend %v, want both keys at one hold of %d", spend, quotaOf(3600))
	}
	rec = a.dashCall(http.MethodGet, "/keys", s, nil)
	a.wantStatus("list keys", rec, http.StatusOK)
	var listed []struct {
		ID                int64   `json:"id"`
		MonthlyCapUSD     *string `json:"monthly_cap_usd"`
		SpentThisMonthUSD *string `json:"spent_this_month_usd"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, l := range listed {
		if l.ID == k.ID && (l.MonthlyCapUSD == nil || *l.MonthlyCapUSD != "2.0000") {
			t.Fatalf("capped key view %+v", l)
		}
		if l.ID == open.ID && l.MonthlyCapUSD != nil {
			t.Fatalf("uncapped key view %+v", l)
		}
		if l.SpentThisMonthUSD != nil {
			t.Fatalf("no ledger is mounted on this harness's dashboard, so spend must be null: %+v", l)
		}
	}
}

func monthStartOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
