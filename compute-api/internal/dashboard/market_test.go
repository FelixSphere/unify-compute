package dashboard

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type fakeMarket struct{ calls []string }

func (f *fakeMarket) record(what string, w http.ResponseWriter, accountID int64) {
	f.calls = append(f.calls, what+" "+strconv.FormatInt(accountID, 10))
	w.WriteHeader(http.StatusTeapot)
}

func (f *fakeMarket) Templates(w http.ResponseWriter, _ *http.Request, id int64) {
	f.record("templates", w, id)
}
func (f *fakeMarket) SKUs(w http.ResponseWriter, _ *http.Request, id int64) { f.record("skus", w, id) }
func (f *fakeMarket) Submit(w http.ResponseWriter, _ *http.Request, id int64) {
	f.record("submit", w, id)
}

func TestMarketRoutesAreNotImplementedWithoutAMarket(t *testing.T) {
	f := newFixture(t)
	s := f.activeUser("ada@example.com")
	for _, rt := range []struct{ method, path string }{{http.MethodGet, "/templates"}, {http.MethodGet, "/skus"}, {http.MethodPost, "/jobs"}} {
		rec := f.do(req{method: rt.method, path: rt.path, cookie: s.cookie, csrf: s.csrf})
		wantErr(t, rec, http.StatusNotImplemented, "not_implemented", "not_implemented")
	}
}

// The marketplace routes reach the job API's handlers with the session's
// account; the submit is a non-GET, so it needs the CSRF header and a
// same-origin Origin before anything runs, like cancel.
func TestMarketRoutesDelegateAndSubmitNeedsCSRF(t *testing.T) {
	f := newFixture(t)
	m := &fakeMarket{}
	f.h.mkt = m
	s := f.activeUser("ada@example.com")
	id := strconv.FormatInt(f.accountOf("ada@example.com"), 10)

	wantErr(t, f.do(req{method: http.MethodPost, path: "/jobs", cookie: s.cookie}), http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodPost, path: "/jobs", cookie: s.cookie, csrf: s.csrf, origin: strp("https://evil.example")}),
		http.StatusForbidden, "permission_error", "csrf_failed")
	wantErr(t, f.do(req{method: http.MethodGet, path: "/templates"}), http.StatusUnauthorized, "authentication_error", "session_required")
	if len(m.calls) != 0 {
		t.Fatalf("refused requests reached the job API: %v", m.calls)
	}
	for _, rt := range []struct{ method, path string }{{http.MethodGet, "/templates"}, {http.MethodGet, "/skus"}, {http.MethodPost, "/jobs"}} {
		if rec := f.do(req{method: rt.method, path: rt.path, cookie: s.cookie, csrf: s.csrf}); rec.Code != http.StatusTeapot {
			t.Fatalf("%s %s: %d %s", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
	if got := strings.Join(m.calls, "|"); got != "templates "+id+"|skus "+id+"|submit "+id {
		t.Fatalf("calls %q", got)
	}
}
