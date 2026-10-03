package api

import (
	"net/http"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

// AccountJobs serves the job API's own job handlers for an account that was
// authenticated some other way: the dashboard's session
// (internal/dashboard, accounts.md "Dashboard API"). Because these are the
// same handlers, the dashboard answers the same Job object, pagination,
// errors, owner scoping (another account's job is 404 job_not_found, like a
// missing one) and 202 cancel as the job API, and cannot drift from it.
//
// What the job API's route wrappers add is not applied here: key auth and
// scopes (the session replaces them) and the per-key rate limit (the
// dashboard limits reads per account itself).
type AccountJobs struct{ s *Server }

func (s *Server) AccountJobs() AccountJobs { return AccountJobs{s: s} }

func (a AccountJobs) as(r *http.Request, accountID int64) *http.Request {
	return r.WithContext(withCaller(r.Context(), caller{owner: accounts.OwnerFor(accountID), billingID: int(accountID)}))
}

func (a AccountJobs) List(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.wired("job listing", a.s.listJobs).ServeHTTP(w, a.as(r, accountID))
}

func (a AccountJobs) Get(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.wired("job lookup", a.s.getJob).ServeHTTP(w, a.as(r, accountID))
}

func (a AccountJobs) Cancel(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.wired("job cancel", a.s.cancelJob).ServeHTTP(w, a.as(r, accountID))
}

func (a AccountJobs) Logs(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.wired("job logs", a.s.jobLogs).ServeHTTP(w, a.as(r, accountID))
}

func (a AccountJobs) Events(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.listEvents(w, a.as(r, accountID))
}

func (a AccountJobs) Artifacts(w http.ResponseWriter, r *http.Request, accountID int64) {
	a.s.listArtifacts(w, a.as(r, accountID))
}
