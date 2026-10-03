package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/api"
	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

// Build is set with -ldflags "-X main.Build=<git sha>" so /healthz can say
// which commit is running. "dev" means nobody set it.
var Build = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cat, err := sku.Load(env("SKU_FILE", "config/skus.json"))
	if err != nil {
		log.Error("load sku catalogue", "err", err)
		os.Exit(1)
	}

	// Quotas are validated at boot like the catalogue, so a bad file fails
	// the deploy. They are enforced only once the job store is open (below).
	quotas, err := quota.Load(env("QUOTA_FILE", "config/quotas.json"))
	if err != nil {
		log.Error("load quotas", "err", err)
		os.Exit(1)
	}

	// Templates are checked against the catalogue they sell from, so a
	// template naming a SKU that was removed fails the deploy, not a submit.
	templates, err := template.Load(env("TEMPLATE_FILE", "config/templates.json"), cat, api.MaxRuntimeSeconds)
	if err != nil {
		log.Error("load templates", "err", err)
		os.Exit(1)
	}

	// Which clouds this deployment may launch into (ADR-0006). Empty is
	// legal and means every submit is refused with 503 until it is set.
	upstreams, err := upstream.Parse(os.Getenv("UPSTREAMS"))
	if err != nil {
		log.Error("upstreams", "err", err)
		os.Exit(1)
	}
	if len(upstreams.Clouds()) == 0 {
		log.Warn("UPSTREAMS unset; job submission will return upstream_unavailable")
	} else {
		log.Info("upstreams enabled", "clouds", upstreams.Clouds())
	}

	// The bridge client is constructed at boot so a bad secret fails the
	// deploy instead of the first job.
	var bridge *console.Client
	if url := os.Getenv("CONSOLE_URL"); url != "" {
		if bridge, err = console.New(url, []byte(os.Getenv("COMPUTE_BRIDGE_SECRET"))); err != nil {
			log.Error("console bridge", "err", err)
			os.Exit(1)
		}
		log.Info("console bridge configured", "url", url)
	} else {
		// No console to verify keys or hold balance against: every keyed
		// job route answers 503 console_unavailable (job-api.md, Errors)
		// once the job store is open. The client below has nowhere to send,
		// so each call fails as an unreachable console does, and the
		// ledger treats it that way (metering.md H5).
		bridge = &console.Client{HTTP: &http.Client{Timeout: 10 * time.Second}, Now: time.Now}
		log.Warn("CONSOLE_URL unset; keyed job routes answer 503 console_unavailable")
	}

	handler := api.New(Build, cat, upstreams, log)
	handler.Templates = templates

	// Artifact storage (ADR-0005). Built at boot so a bad configuration
	// fails the deploy rather than the first upload. The token secret is the
	// switch: hosts get the bucket from Terraform but the secret only once
	// someone creates it, and a host without one must still boot (the
	// CONSOLE_URL crash loop of 2026-10-02 is the lesson). The routes answer
	// 501 until the job store and owner resolution are wired as well.
	var art *artifacts.Service
	bucket, tokenSecret := os.Getenv("ARTIFACTS_BUCKET"), os.Getenv("ARTIFACTS_TOKEN_SECRET")
	if bucket != "" && tokenSecret != "" {
		region := env("AWS_REGION", "us-east-1")
		var creds awsv4.CredentialSource = &awsv4.IMDS{}
		if c, ok := awsv4.FromEnv(); ok {
			creds = c
		}
		art, err = artifacts.New(artifacts.Config{
			Bucket:        bucket,
			WriterRoleARN: os.Getenv("ARTIFACTS_WRITER_ROLE_ARN"),
			PublicURL:     os.Getenv("PUBLIC_URL"),
			TokenSecret:   []byte(tokenSecret),
			Creds:         creds,
			Client:        &awsv4.Client{Region: region},
		})
		if err != nil {
			log.Error("artifacts", "err", err)
			os.Exit(1)
		}
		handler.Artifacts = art
		log.Info("artifact storage configured", "bucket", bucket)
	} else {
		log.Warn("ARTIFACTS_BUCKET or ARTIFACTS_TOKEN_SECRET unset; spec.outputs cannot be uploaded and artifact routes answer 501")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The job store is the switch between the dark build and a working one.
	// Without DATABASE_URL every job route keeps answering 501 after
	// validation, as before this was wired.
	var svc *jobs.Service
	if dsn := os.Getenv("DATABASE_URL"); dsn == "" {
		log.Warn("DATABASE_URL unset; job routes answer 501 not_implemented (dark build)", "quotas_enforced", false)
	} else {
		st, err := openStore(ctx, log, dsn)
		if err != nil {
			log.Error("job store", "err", err)
			os.Exit(1)
		}
		defer st.Close()
		var sky *backend.Client
		if url := os.Getenv("SKYPILOT_API_SERVER_URL"); url != "" {
			sky = &backend.Client{BaseURL: url, Token: os.Getenv("SKYPILOT_SERVICE_ACCOUNT_TOKEN"), UserHash: skyPilotUser,
				HTTP: &http.Client{Timeout: 2 * time.Minute}}
			log.Info("skypilot api server configured", "url", url)
		} else {
			log.Warn("SKYPILOT_API_SERVER_URL unset; job submission answers 503 upstream_unavailable")
		}
		cfg := jobs.Config{
			Store: st, Console: bridge, SkyPilot: sky, Catalogue: cat, Log: log,
			Quota: &quota.Enforcer{Config: quotas, Catalogue: cat, Usage: st},
		}
		if art != nil {
			cfg.Outputs = art
		}
		svc = jobs.New(cfg)
		verifier := &api.Verifier{Console: bridge, Log: log}
		handler.Verifier, handler.JobService = verifier, svc
		handler.Owner, handler.Events, handler.Jobs = verifier.Owner, st, st
		log.Info("job store open", "quotas_enforced", true, "overrides", len(quotas.Owners))
		// Attempts live in this process's memory (backend.md open question
		// 2), so a job that was live when the last process stopped is not
		// observed again. Say so rather than let it look tracked.
		if live, err := st.ActiveJobs(ctx); err != nil {
			log.Error("read live jobs", "err", err)
		} else if len(live) > 0 {
			for _, j := range live {
				log.Warn("live job from an earlier process is not resumed", "job", j.ID, "status", j.Status, "owner", j.Owner)
			}
		}
	}

	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", "127.0.0.1:8080"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	var workers sync.WaitGroup
	if svc != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			svc.Run(ctx)
		}()
	}
	go func() {
		log.Info("listening", "addr", srv.Addr, "build", Build, "skus", len(cat.SKUs), "templates", len(templates.Templates))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	// The loops stop at their next tick boundary; a step in flight finishes.
	workers.Wait()
}

// skyPilotUser is the SkyPilot user every cluster belongs to, so any
// compute-api process can observe and tear down any job's cluster
// (backend.md, "Common body fields"). Changing it orphans live clusters.
const skyPilotUser = "unifycmp"

// openStore connects, retrying for a minute so a database that is still
// starting does not fail the boot, and migrates. Migrate takes an advisory
// lock per migration, so instances booting together apply each once.
func openStore(ctx context.Context, log *slog.Logger, dsn string) (*store.Store, error) {
	deadline := time.Now().Add(time.Minute)
	for {
		st, err := store.Open(ctx, dsn)
		if err == nil {
			if err := st.Migrate(ctx); err != nil {
				st.Close()
				return nil, err
			}
			return st, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, err
		}
		log.Warn("job store not reachable yet; retrying", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
