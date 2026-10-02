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
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
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
