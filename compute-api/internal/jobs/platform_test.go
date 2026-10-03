package jobs_test

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
)

// The submit flow with the shipped platform limits (one active job across
// every account, ADR-0010 "Start small"): a second account's submit is
// refused with the platform violation, before the console is asked for a
// hold.
func TestSubmitHonoursThePlatformWideLimit(t *testing.T) {
	w := newWorld(t)
	platform, err := quota.LoadPlatform(filepath.Join("..", "..", "config", "platform.json"))
	w.must(err)
	quotas, err := quota.Load(filepath.Join("..", "..", "config", "quotas.json"))
	w.must(err)
	w.console.SeedUser(consoletest.User{ID: 2002, Quota: 1_000_000_000})

	svc := jobs.New(jobs.Config{
		Store: w.st, Console: w.console.Client(), Catalogue: w.cat, Now: w.clock.Now,
		SkyPilot: &backend.Client{BaseURL: w.sky.URL, Token: skyToken, UserHash: "unifycmp"},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Quota:    &quota.Enforcer{Config: quotas, Catalogue: w.cat, Usage: w.st, Platform: platform},
	})
	w.submit(svc, 600)

	holds := len(w.console.CallsTo(consoletest.RouteHold))
	sub := jobs.Submission{
		Owner: "user:2002", UserID: 2002, TokenID: 22, SKU: w.item, Candidates: w.item.Providers,
		Spec: backend.Spec{Image: "ghcr.io/acme/embed:1.4", Command: []string{"python", "run.py"}, MaxRuntimeSeconds: 600},
	}
	_, _, err = svc.Submit(w.ctx, sub)
	var ex *quota.ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("second account's submit: %v, want *quota.ExceededError", err)
	}
	var platformRefused bool
	for _, v := range ex.Violations {
		if v.Limit == quota.LimitPlatformActiveJobs && v.Used == 1 && v.Max == 1 && v.Headroom == 0 {
			platformRefused = true
		}
	}
	if !platformRefused {
		t.Fatalf("violations %+v, want platform_max_active_jobs 1 of 1", ex.Violations)
	}
	if n := len(w.console.CallsTo(consoletest.RouteHold)); n != holds {
		t.Fatalf("a submit over the platform limit asked the console for a hold (%d -> %d)", holds, n)
	}

}
