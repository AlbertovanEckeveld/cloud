// Command platform runs the private-cloud platform: one binary that serves the
// API and the embedded frontend.
//
// Usage:
//
//	platform                       run the server (configured via environment variables)
//	platform user create ...       create an account and print its activation link
//	platform user reset ...        reset an account's credentials and print a new activation link
//	platform healthcheck           probe /readyz of a running server; for container health checks
package main

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/health"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
	"github.com/AlbertovanEckeveld/cloud/internal/core/logging"
	"github.com/AlbertovanEckeveld/cloud/internal/core/mailer"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/internal/core/site"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
	"github.com/AlbertovanEckeveld/cloud/internal/drive"
	"github.com/AlbertovanEckeveld/cloud/internal/identity"
	"github.com/AlbertovanEckeveld/cloud/migrations"
	"github.com/AlbertovanEckeveld/cloud/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// modules lists every module with migrations, in dependency order.
var modules = []string{"core", "identity", "drive"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	args := os.Args[1:]
	switch {
	case len(args) == 0:
		err = serve(ctx)
	case args[0] == "healthcheck":
		os.Exit(healthcheck(os.Getenv("HTTP_ADDR")))
	case args[0] == "user":
		err = userCommand(ctx, os.Stdout, args[1:])
	default:
		err = fmt.Errorf("unknown command %q (commands: user, healthcheck)", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform:", err)
		os.Exit(1)
	}
}

// app holds what both the server and the CLI need.
type app struct {
	cfg      config.Config
	logger   *slog.Logger
	pool     *pgxpool.Pool
	store    *storage.Client
	mailer   *mailer.Mailer
	identity *identity.Service
}

// setup loads configuration, connects to the database and migrates it.
func setup(ctx context.Context, logOutput io.Writer) (*app, error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, err
	}
	logger := logging.New(logOutput, cfg.LogLevel, cfg.Env == config.EnvDevelopment)
	slog.SetDefault(logger)

	startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := database.Open(startupCtx, cfg.Database)
	if err != nil {
		return nil, err
	}
	var mods []database.Module
	for _, name := range modules {
		fsys, err := migrations.For(name)
		if err != nil {
			pool.Close()
			return nil, err
		}
		mods = append(mods, database.Module{Name: name, FS: fsys})
	}
	if err := database.Migrate(startupCtx, pool, logger, mods...); err != nil {
		pool.Close()
		return nil, err
	}

	sl, err := sealer.New([]byte(cfg.EncryptionKey.Reveal()))
	if err != nil {
		pool.Close()
		return nil, err
	}
	store, err := storage.New(cfg.Storage)
	if err != nil {
		pool.Close()
		return nil, err
	}
	mail, err := mailer.New(pool, sl, cfg.Mail, cfg.PublicURL.Hostname(), logger)
	if err != nil {
		pool.Close()
		return nil, err
	}
	// A separate key for the OIDC provider's access tokens, derived from the
	// platform key (HKDF, RFC 5869) so one key never serves two purposes.
	tokenKey, err := hkdf.Key(sha256.New, []byte(cfg.EncryptionKey.Reveal()), nil, "cloud oidc access tokens", 32)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("derive oidc key: %w", err)
	}
	opts := identity.Options{SiteID: cfg.SiteID, PublicURL: cfg.PublicURL, Mailer: mail, Logger: logger,
		OIDCTokenKey: [32]byte(tokenKey), DevMode: cfg.Env == config.EnvDevelopment, Objects: store}
	if cfg.Env == config.EnvDevelopment {
		// Passkeys are bound to an origin; allow the Vite dev server of `make dev`.
		opts.ExtraOrigins = []string{"http://localhost:5173"}
	}
	id, err := identity.New(pool, sl, opts)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &app{cfg: cfg, logger: logger, pool: pool, store: store, mailer: mail, identity: id}, nil
}

func serve(ctx context.Context) error {
	a, err := setup(ctx, os.Stdout)
	if err != nil {
		return err
	}
	defer a.pool.Close()
	cfg, logger := a.cfg, a.logger
	logger.Info("starting platform", "version", version, "site", cfg.SiteID, "env", cfg.Env, "public_url", cfg.PublicURL.String())
	if cfg.Env == config.EnvDevelopment {
		logger.Warn("development mode: unencrypted connections are allowed")
	}

	startupCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	store := a.store
	if err := store.Ping(startupCtx); err != nil {
		return fmt.Errorf("object storage: %w", err)
	}

	// Claim the port before starting background work, so a port conflict
	// fails fast without anything left running.
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	defer ln.Close()

	registry := site.NewRegistry(a.pool, cfg.SiteID, version, logger)
	if err := registry.Heartbeat(startupCtx); err != nil {
		return err
	}
	opts := drive.Options{Logger: logger, Sharing: a.identity}
	var frameOrigins []string
	if cfg.Collabora != nil {
		// A separate key per purpose: a leaked WOPI key cannot forge OIDC tokens.
		wopiKey, err := hkdf.Key(sha256.New, []byte(cfg.EncryptionKey.Reveal()), nil, "cloud wopi access tokens", 32)
		if err != nil {
			return fmt.Errorf("derive wopi key: %w", err)
		}
		opts.Office = &drive.Office{URL: cfg.Collabora, PublicURL: cfg.PublicURL, Key: [32]byte(wopiKey)}
		frameOrigins = append(frameOrigins, cfg.Collabora.String())
	}
	files := drive.New(a.pool, store, opts)
	go registry.Run(ctx)
	go a.identity.Run(ctx)
	go files.Run(ctx)
	if a.mailer.Enabled() {
		// A wrong password or unreachable server should show up in the log
		// right away, but must neither stop nor delay startup: mail is queued
		// and retried anyway.
		go func() {
			verifyCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := a.mailer.Verify(verifyCtx); err != nil {
				logger.Warn("mail server check failed; mail stays queued until it works", "error", err)
				return
			}
			logger.Info("mail server reachable", "host", cfg.Mail.Host)
		}()
		go a.mailer.Run(ctx)
	} else {
		logger.Info("mail disabled: no SMTP_HOST configured")
	}

	checks := []health.Check{
		{Name: "database", Run: a.pool.Ping},
		{Name: "storage", Run: store.Ping},
	}
	if cfg.Collabora != nil {
		checks = append(checks, health.Check{Name: "collabora", Run: files.OfficeCheck, Optional: true})
	}
	checker := health.NewChecker(logger, checks...)

	oidcProvider, err := a.identity.OIDCHandler(startupCtx)
	if err != nil {
		return err
	}

	dist, err := web.Dist()
	if err != nil {
		return fmt.Errorf("frontend: %w", err)
	}
	spa, err := httpserver.NewSPA(dist, frameOrigins...)
	if err != nil {
		return err
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
		oidc:      oidcProvider,
		avatars:   a.identity.AvatarHandler,
		drive: func(r chi.Router) {
			files.Routes(r, drive.Auth{
				RequireUser:       a.identity.RequireUserOrDevice,
				RequireAdmin:      identity.RequireAdmin,
				RequireRecentAuth: a.identity.RequireRecentAuth,
				Audit:             a.identity.AuditRequest,
				UserID: func(r *http.Request) uuid.UUID {
					p, _ := identity.PrincipalFrom(r.Context())
					return p.User.ID
				},
				Desktop: func(r *http.Request) bool {
					p, _ := identity.PrincipalFrom(r.Context())
					return p.Device
				},
			})
		},
		wopi: files.WOPIRoutes,
		dav: func(r chi.Router) {
			files.DAVRoutes(r, a.identity.RequireAppPassword("Cloud"), func(r *http.Request) uuid.UUID {
				p, _ := identity.PrincipalFrom(r.Context())
				return p.User.ID
			})
		},
		identity:     a.identity.Routes,
		requireUser:  a.identity.RequireUser,
		requireAdmin: identity.RequireAdmin,
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
