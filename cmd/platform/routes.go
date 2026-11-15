package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/health"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
	"github.com/AlbertovanEckeveld/cloud/internal/core/site"
)

// routerDeps are the collaborators the HTTP routes need. Interfaces keep the
// router testable without a database.
type routerDeps struct {
	logger    *slog.Logger
	hsts      bool
	siteID    string
	version   string
	startedAt time.Time
	health    healthReporter
	sites     siteLister
	frontend  http.Handler
}

type healthReporter interface {
	Report(ctx context.Context) health.Report
	Liveness(w http.ResponseWriter, r *http.Request)
	Readiness(w http.ResponseWriter, r *http.Request)
}

type siteLister interface {
	List(ctx context.Context) ([]site.Site, error)
}

func newRouter(d routerDeps) http.Handler {
	r := chi.NewRouter()
	r.Use(
		httpserver.RequestID,
		httpserver.AccessLog(d.logger),
		httpserver.SecurityHeaders(d.hsts),
		httpserver.Recover(d.logger),
	)

	r.Get("/healthz", d.health.Liveness)
	r.Get("/readyz", d.health.Readiness)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(httpserver.NoStore)
		r.Get("/status", statusHandler(d))
		r.Get("/sites", sitesHandler(d))
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			httpserver.WriteError(w, http.StatusNotFound, "not_found", "Not found")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			httpserver.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		})
	})

	r.Handle("/*", d.frontend)
	return r
}

type statusResponse struct {
	SiteID    string          `json:"site_id"`
	Version   string          `json:"version"`
	StartedAt time.Time       `json:"started_at"`
	OK        bool            `json:"ok"`
	Checks    []health.Result `json:"checks"`
	CheckedAt time.Time       `json:"checked_at"`
}

func statusHandler(d routerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rep := d.health.Report(r.Context())
		httpserver.WriteJSON(w, http.StatusOK, statusResponse{
			SiteID:    d.siteID,
			Version:   d.version,
			StartedAt: d.startedAt,
			OK:        rep.OK,
			Checks:    rep.Checks,
			CheckedAt: rep.CheckedAt,
		})
	}
}

func sitesHandler(d routerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sites, err := d.sites.List(r.Context())
		if err != nil {
			d.logger.Error("list sites", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
			httpserver.WriteError(w, http.StatusServiceUnavailable, "unavailable", "Site list is temporarily unavailable")
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, struct {
			Sites []site.Site `json:"sites"`
		}{sites})
	}
}
