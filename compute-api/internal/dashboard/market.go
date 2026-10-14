package dashboard

import (
	"net/http"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
)

// MarketRoutes is the job API's catalogue and submit, served for the
// session's account (api.AccountJobs): the dashboard's marketplace page
// (spec/marketplace.md M3) reads the templates and SKUs and submits a
// template run with the session instead of a key. Bodies and errors are the
// job API's own; the session submit has no key, so no key cap applies and
// the job's token id is null.
type MarketRoutes interface {
	Templates(w http.ResponseWriter, r *http.Request, accountID int64)
	SKUs(w http.ResponseWriter, r *http.Request, accountID int64)
	Submit(w http.ResponseWriter, r *http.Request, accountID int64)
}

func (h *Handler) market(route func(MarketRoutes, http.ResponseWriter, *http.Request, int64)) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ accounts.Session, u accounts.User) {
		if h.mkt == nil {
			notImplemented(w, "Running templates from the dashboard is")
			return
		}
		route(h.mkt, w, r, u.AccountID)
	}
}
