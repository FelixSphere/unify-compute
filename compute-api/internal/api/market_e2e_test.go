package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/dashboard"
)

// The dashboard's marketplace routes (spec/marketplace.md M3) end to end:
// the catalogue through the session, and a template run submitted with the
// session instead of a key, on the ACCOUNTS_BACKEND=local harness wired as
// main wires it.
func newMarketE2E(t *testing.T) *acctE2E {
	t.Helper()
	a := newAccountsE2E(t)
	dash, err := dashboard.New(dashboard.Config{Accounts: a.acc, Mailer: a.m, AppOrigin: acctOrigin,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: a.clock.Now,
		Jobs: a.srv.AccountJobs(), Market: a.srv.AccountJobs(), Ledger: a.bill})
	if err != nil {
		t.Fatal(err)
	}
	a.srv.Dashboard, a.dash = dash, dash
	return a
}

func TestDashboardTemplateRunEndToEnd(t *testing.T) {
	a := newMarketE2E(t)
	s, accountID := a.signupAndLogin("mia@example.com")

	// The catalogue, as the job API serves it, on the session.
	body := a.dashGet("/templates", s, nil)
	if !strings.Contains(body, `"id":"batch-infer-vllm"`) || !strings.Contains(body, `"publisher":"Unify Compute"`) {
		t.Fatalf("templates: %s", body)
	}
	if body := a.dashGet("/skus", s, nil); !strings.Contains(body, `"id":"a10g-24gb-x1"`) {
		t.Fatalf("skus: %s", body)
	}
	if rec := a.dashCall(http.MethodGet, "/templates", dashSession{}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("templates without a session: %d", rec.Code)
	}

	submit := json.RawMessage(templateBody(""))
	dashErr(t, "submit without CSRF", a.dashCall(http.MethodPost, "/jobs", dashSession{cookie: s.cookie}, submit), http.StatusForbidden, "csrf_failed")
	// Compute is off by default (ADR-0011 D3): the same refusal as a key
	// submit, and nothing moves.
	dashErr(t, "submit before enable-compute", a.dashCall(http.MethodPost, "/jobs", s, submit), http.StatusForbidden, "compute_not_enabled")
	if launches := a.sky.Launches(); len(launches) != 0 {
		t.Fatalf("launched %+v", launches)
	}
	dashErr(t, "unknown template", a.dashCall(http.MethodPost, "/jobs", s, json.RawMessage(`{"template":"nope"}`)), http.StatusBadRequest, "unknown_template")

	id := strconv.FormatInt(accountID, 10)
	a.admin("account", "enable-compute", id, "--reason", "e2e: let mia run templates")
	a.admin("balance", "credit", id, "--usd", "5", "--reason", "e2e: starting credit")

	rec := a.dashCall(http.MethodPost, "/jobs", s, submit)
	a.wantStatus("submit", rec, http.StatusCreated)
	var j struct {
		ID     string `json:"id"`
		SKU    string `json:"sku"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || !strings.HasPrefix(j.ID, "job_") || j.SKU != "a10g-24gb-x1" {
		t.Fatalf("submitted job %s (%v)", rec.Body, err)
	}
	// The job is the account's, submitted by no key, from the template.
	stored, err := a.st.Get(a.ctx, accounts.OwnerFor(accountID), j.ID)
	a.must(err)
	if stored.TokenID != nil || stored.TemplateID == nil || *stored.TemplateID != "batch-infer-vllm" {
		t.Fatalf("stored job token %v template %v", stored.TokenID, stored.TemplateID)
	}
	hd, err := a.svc.Ledger.Hold(a.ctx, j.ID)
	a.must(err)
	hr, ok, err := a.bill.HoldRecord(a.ctx, hd.HoldID)
	a.must(err)
	if !ok || hr.AccountID != accountID || hr.KeyID != 0 || hr.Status != "open" {
		t.Fatalf("billing hold %+v %v", hr, ok)
	}
	if b := a.balance(accountID); b.Held != quotaOf(3600) {
		t.Fatalf("balance with the hold %+v", b)
	}
	// The job page's reads see it, like any job of the account.
	if body := a.dashGet("/jobs/"+j.ID, s, nil); !strings.Contains(body, `"id":"`+j.ID+`"`) {
		t.Fatalf("job: %s", body)
	}
	if launches := a.sky.Launches(); len(launches) != 1 {
		t.Fatalf("launches %+v", launches)
	}
}
