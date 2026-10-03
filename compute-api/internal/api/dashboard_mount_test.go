package api

import (
	"net/http"
	"testing"
)

// With the console backend (no Dashboard) /dashboard/v1 is the mux's 404,
// exactly as before the dashboard existed; with one, every path under the
// prefix is handed to it and nothing else is.
func TestDashboardMount(t *testing.T) {
	s := newTestServer(t, "aws")
	for _, p := range []string{"/dashboard/v1/config", "/dashboard/v1/login", "/dashboard/v1"} {
		if rec := do(t, s, http.MethodGet, p, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("no dashboard: GET %s = %d", p, rec.Code)
		}
	}

	var got []string
	s.Dashboard = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	for _, p := range []string{"/dashboard/v1", "/dashboard/v1/config", "/dashboard/v1/password/forgot"} {
		if rec := do(t, s, http.MethodPost, p, "", ""); rec.Code != http.StatusTeapot {
			t.Errorf("POST %s = %d, want the dashboard", p, rec.Code)
		}
	}
	for _, p := range []string{"/healthz", "/dashboard/v10/config", "/dashboardx"} {
		do(t, s, http.MethodGet, p, "", "")
	}
	if len(got) != 3 {
		t.Errorf("dashboard saw %v", got)
	}
	if rec := do(t, s, http.MethodGet, "/healthz", "", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz %d", rec.Code)
	}
}
