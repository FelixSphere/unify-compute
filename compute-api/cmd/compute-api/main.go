package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/api"
	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
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
	// the deploy. They are not enforced yet: enforcement needs the owner a
	// key resolves to, which arrives with the console bridge's verify, and
	// the job store, which this binary does not open yet.
	quotas, err := quota.Load(env("QUOTA_FILE", "config/quotas.json"))
	if err != nil {
		log.Error("load quotas", "err", err)
		os.Exit(1)
	}
	log.Info("quotas loaded", "overrides", len(quotas.Owners), "enforced", false)

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
	// deploy instead of the first job. It is not called by any route yet.
	if url := os.Getenv("CONSOLE_URL"); url != "" {
		if _, err := console.New(url, []byte(os.Getenv("COMPUTE_BRIDGE_SECRET"))); err != nil {
			log.Error("console bridge", "err", err)
			os.Exit(1)
		}
		log.Info("console bridge configured", "url", url)
	} else {
		log.Warn("CONSOLE_URL unset; job routes will refuse until it is")
	}

	handler := api.New(Build, cat, upstreams, log)
	handler.Templates = templates

	// Artifact storage (ADR-0005). Built at boot so a half-configured
	// environment fails the deploy rather than the first upload. The routes
	// still answer 501 until the job store and owner resolution are wired.
	if bucket := os.Getenv("ARTIFACTS_BUCKET"); bucket != "" {
		region := env("AWS_REGION", "us-east-1")
		var creds awsv4.CredentialSource = &awsv4.IMDS{}
		if c, ok := awsv4.FromEnv(); ok {
			creds = c
		}
		art, err := artifacts.New(artifacts.Config{
			Bucket:        bucket,
			WriterRoleARN: os.Getenv("ARTIFACTS_WRITER_ROLE_ARN"),
			PublicURL:     os.Getenv("PUBLIC_URL"),
			TokenSecret:   []byte(os.Getenv("ARTIFACTS_TOKEN_SECRET")),
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
		log.Warn("ARTIFACTS_BUCKET unset; spec.outputs cannot be uploaded and artifact routes answer 501")
	}

	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", "127.0.0.1:8080"),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
