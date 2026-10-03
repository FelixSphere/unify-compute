package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/admin"
	"github.com/FelixSphere/unify-compute/compute-api/internal/api"
	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/dashboard"
	"github.com/FelixSphere/unify-compute/compute-api/internal/jobs"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
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
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdmin(os.Args[2:]))
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Who keeps balances and holds (ADR-0011): the console bridge until
	// the cutover, compute's own local biller after it. Read first so a
	// typo fails the deploy before anything else is built.
	backendName, err := accountsBackend()
	if err != nil {
		log.Error("accounts backend", "err", err)
		os.Exit(1)
	}

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

	// Platform-wide limits and the runtime ceiling (ADR-0010, "Start
	// small"). Required: a deployment without them would have no cap on
	// GPU spend in flight beyond the per-owner quotas.
	platform, err := quota.LoadPlatform(env("PLATFORM_FILE", "config/platform.json"))
	if err != nil {
		log.Error("load platform limits", "err", err)
		os.Exit(1)
	}

	// Templates are checked against the catalogue they sell from, so a
	// template naming a SKU that was removed fails the deploy, not a submit.
	templates, err := template.Load(env("TEMPLATE_FILE", "config/templates.json"), cat, platform.MaxRuntimeSeconds)
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
	if backendName == backendLocal {
		// Holds are local, and so must be the identity they are placed
		// against: a console user id is not an account id. Keys are
		// compute's own uck_ keys (below); the console is never called.
		bridge = &console.Client{HTTP: &http.Client{Timeout: 10 * time.Second}, Now: time.Now}
		if os.Getenv("CONSOLE_URL") != "" {
			log.Warn("ACCOUNTS_BACKEND=local: CONSOLE_URL is ignored")
		}
	} else if url := os.Getenv("CONSOLE_URL"); url != "" {
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
	handler.RuntimeCeilingSeconds = platform.MaxRuntimeSeconds

	// Artifact storage (ADR-0005). Built at boot so a bad configuration
	// fails the deploy rather than the first upload. The token secret is the
	// switch: hosts get the bucket from Terraform but the secret only once
	// someone creates it, and a host without one must still boot (the
	// CONSOLE_URL crash loop of 2026-10-02 is the lesson). The routes answer
	// 501 until the job store and owner resolution are wired as well.
	creds := awsCredentials(log)
	var art *artifacts.Service
	bucket, tokenSecret := os.Getenv("ARTIFACTS_BUCKET"), os.Getenv("ARTIFACTS_TOKEN_SECRET")
	if bucket != "" && tokenSecret != "" {
		art, err = artifacts.New(artifacts.Config{
			Bucket:        bucket,
			WriterRoleARN: os.Getenv("ARTIFACTS_WRITER_ROLE_ARN"),
			PublicURL:     os.Getenv("PUBLIC_URL"),
			TokenSecret:   []byte(tokenSecret),
			Creds:         creds,
			Client:        &awsv4.Client{Region: env("AWS_REGION", "us-east-1")},
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

	// Account mail (ADR-0011). With ACCOUNTS_BACKEND=local a deployment that
	// cannot send refuses to start: Unify Data's silent outbox fallback
	// meant production signups that never got their code. The dashboard
	// routes below are its only caller.
	mailSettings := mailer.SettingsFromEnv(os.Getenv)
	mail, err := mailer.New(mailSettings, &awsv4.Client{Region: env("AWS_REGION", "us-east-1")}, creds)
	if err != nil {
		log.Error("mailer", "err", err)
		os.Exit(1)
	}
	switch {
	case mail == nil:
		log.Info("mailer off: SES_SENDER unset and ACCOUNTS_BACKEND is not local")
	case mailSettings.Required():
		if err := mail.Transport.Check(ctx); err != nil {
			log.Error("mailer cannot send", "err", err)
			os.Exit(1)
		}
		fallthrough
	default:
		switch t := mail.Transport.(type) {
		case *mailer.Outbox:
			log.Warn("mailer writes to an outbox directory, not SES (ENVIRONMENT=development)", "dir", t.Dir)
		case *mailer.SES:
			log.Info("mailer sends through SES", "from", mail.From.String(), "configuration_set", t.ConfigurationSet)
		}
	}

	// The job store is the switch between the dark build and a working one.
	// Without DATABASE_URL every job route keeps answering 501 after
	// validation, as before this was wired.
	var svc *jobs.Service
	var dash *dashboard.Handler
	var sweepAccounts *accounts.Service
	if dsn := os.Getenv("DATABASE_URL"); dsn == "" {
		if backendName == backendLocal {
			log.Error("ACCOUNTS_BACKEND=local needs DATABASE_URL: the balance, accounts and sessions live in the job store")
			os.Exit(1)
		}
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
		// Elect: only the process holding the store's worker lock drives
		// jobs, so the old and the new process of a rolling restart never
		// both do (backend.md, "Restart").
		cfg := jobs.Config{
			Store: st, Console: bridge, SkyPilot: sky, Catalogue: cat, Log: log, Elect: true,
			Quota: &quota.Enforcer{Config: quotas, Catalogue: cat, Usage: st, Platform: platform},
		}
		if art != nil {
			cfg.Outputs = art
		}
		if backendName == backendLocal {
			local := billing.New(st.Pool(), nil)
			cfg.Biller, cfg.Gate = local, local
		}
		svc = jobs.New(cfg)
		handler.JobService, handler.Events, handler.Jobs = svc, st, st
		var acc *accounts.Service
		if backendName == backendLocal {
			// Only uck_ keys, looked up on every request; sk- keys are
			// missing_key and nothing reaches the console.
			acc = accounts.New(st.Pool(), accounts.Config{ComputeEnabledDefault: os.Getenv("ACCOUNTS_COMPUTE_DEFAULT") == "true"})
			handler.Keys, handler.Owner = acc, api.KeyOwner(acc)
		} else {
			verifier := &api.Verifier{Console: bridge, Log: log}
			handler.Verifier, handler.Owner = verifier, verifier.Owner
		}
		log.Info("job store open", "accounts_backend", backendName, "quotas_enforced", true, "overrides", len(quotas.Owners),
			"platform_max_active_jobs", platform.MaxActiveJobs, "platform_max_gpus", platform.MaxGPUs,
			"max_runtime_seconds", platform.MaxRuntimeSeconds)
		// The same switch mounts /dashboard/v1: with the console backend
		// none of it is registered.
		if backendName == backendLocal {
			dash, err = dashboard.New(dashboard.Config{Accounts: acc, Mailer: mail, AppOrigin: os.Getenv("APP_ORIGIN"), Log: log})
			if err != nil {
				log.Error("dashboard", "err", err)
				os.Exit(1)
			}
			handler.Dashboard = dash
			sweepAccounts = acc
			log.Info("local accounts: /dashboard/v1 served on the app host", "app_origin", os.Getenv("APP_ORIGIN"))
		}
		// Before serving: a process that gets the lock now takes up every
		// job an earlier process left live, from the ledger and SkyPilot.
		// One that does not serves reads and submits and keeps trying.
		svc.Elect(ctx)
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
	if sweepAccounts != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			sweepAccountsHourly(ctx, log, sweepAccounts)
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
	if dash != nil {
		// Reset links and codes queued after a response still go out.
		dash.Wait()
	}
	// The loops stop at their next tick boundary; a step in flight finishes.
	workers.Wait()
}

// sweepAccountsHourly deletes expired sessions, codes, reset tokens and
// throttle rows, and accounts never verified within 7 days (spec/accounts.md).
// Every instance may run it: each delete is idempotent.
func sweepAccountsHourly(ctx context.Context, log *slog.Logger, acc *accounts.Service) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := acc.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
			log.Error("accounts sweep: expired rows", "err", err)
		}
		if n, err := acc.PurgeUnverified(ctx); err != nil && ctx.Err() == nil {
			log.Error("accounts sweep: unverified accounts", "err", err)
		} else if n > 0 {
			log.Info("accounts sweep: removed unverified accounts", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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

// awsCredentials is the instance role: from the file the host refreshes on
// the hosted environments (ADR-0005, amendment of 2026-10-03), from IMDS
// elsewhere on EC2, or from the AWS_* variables off EC2.
func awsCredentials(log *slog.Logger) awsv4.CredentialSource {
	if c, ok := awsv4.FromEnv(); ok {
		return c
	}
	if path := os.Getenv("INSTANCE_ROLE_CREDENTIALS_FILE"); path != "" {
		log.Info("aws credentials from file", "path", path)
		return &awsv4.File{Path: path}
	}
	return &awsv4.IMDS{}
}

const (
	backendConsole = "console"
	backendLocal   = "local"
)

func accountsBackend() (string, error) {
	switch v := env("ACCOUNTS_BACKEND", backendConsole); v {
	case backendConsole, backendLocal:
		return v, nil
	default:
		return "", fmt.Errorf("ACCOUNTS_BACKEND must be %q or %q, got %q", backendConsole, backendLocal, v)
	}
}

// runAdmin is `compute-api admin …` (internal/admin), run on the host with
// the service's environment. It opens the job store without migrating.
func runAdmin(args []string) int {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "compute-api admin: DATABASE_URL is not set; run it with the service's environment")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compute-api admin:", err)
		return 1
	}
	defer st.Close()
	cli := &admin.CLI{DB: st.Pool(), Accounts: accounts.New(st.Pool(), accounts.Config{}), Biller: billing.New(st.Pool(), nil),
		Actor: operator(), Out: os.Stdout}
	// The same mailer the service builds; nil when this deployment sends no
	// account mail, and enable-compute then says no email was sent.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mail, err := mailer.New(mailer.SettingsFromEnv(os.Getenv), &awsv4.Client{Region: env("AWS_REGION", "us-east-1")}, awsCredentials(quiet))
	if err != nil {
		fmt.Fprintln(os.Stderr, "compute-api admin: mailer:", err)
		return 1
	}
	if mail != nil {
		cli.Mailer = mail
	}
	err = cli.Run(ctx, args)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, admin.ErrUsage):
		fmt.Fprintf(os.Stderr, "compute-api admin: %v\n\n%s\n", err, admin.Usage)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "compute-api admin:", err)
		return 1
	}
}

// operator names who ran an admin command for the audit trail: the person
// behind sudo when there is one, else the login.
func operator() string {
	for _, k := range []string{"SUDO_USER", "USER"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "unknown"
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
