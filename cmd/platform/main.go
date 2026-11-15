// Command platform runs the private-cloud platform: one binary that serves the
// API and the embedded frontend.
//
// Usage:
//
//	platform              run the server (configured via environment variables)
//	platform healthcheck  probe /readyz of a running server; for container health checks
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/health"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
	"github.com/AlbertovanEckeveld/cloud/internal/core/logging"
	"github.com/AlbertovanEckeveld/cloud/internal/core/site"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
	"github.com/AlbertovanEckeveld/cloud/migrations"
	"github.com/AlbertovanEckeveld/cloud/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(os.Getenv("HTTP_ADDR")))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "platform:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel, cfg.Env == config.EnvDevelopment)
	slog.SetDefault(logger)
	logger.Info("starting platform", "version", version, "site", cfg.SiteID, "env", cfg.Env)
	if cfg.Env == config.EnvDevelopment {
		logger.Warn("development mode: unencrypted connections to database and storage are allowed")
	}

	startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pool, err := database.Open(startupCtx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	coreMigrations, err := migrations.For("core")
	if err != nil {
		return err
	}
	if err := database.Migrate(startupCtx, pool, logger, database.Module{Name: "core", FS: coreMigrations}); err != nil {
		return err
	}

	store, err := storage.New(cfg.Storage)
	if err != nil {
		return err
	}
	if err := store.Ping(startupCtx); err != nil {
		return fmt.Errorf("object storage: %w", err)
	}

	registry := site.NewRegistry(pool, cfg.SiteID, version, logger)
	if err := registry.Heartbeat(startupCtx); err != nil {
		return err
	}
	go registry.Run(ctx)

	checker := health.NewChecker(logger,
		health.Check{Name: "database", Run: pool.Ping},
		health.Check{Name: "storage", Run: store.Ping},
	)

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("frontend: %w", err)
	}
	spa, err := httpserver.NewSPA(dist)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}

	router := newRouter(routerDeps{
		logger:    logger,
		hsts:      cfg.Env == config.EnvProduction,
		siteID:    cfg.SiteID,
		version:   version,
		startedAt: time.Now().UTC(),
		health:    checker,
		sites:     registry,
		frontend:  spa,
	})
	return httpserver.Run(ctx, logger, ln, router, checker.Drain)
}

// healthcheck probes /readyz on the local server and returns a process exit
// code. Distroless images have no curl, so the binary checks itself.
func healthcheck(addr string) int {
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: invalid HTTP_ADDR:", err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}
