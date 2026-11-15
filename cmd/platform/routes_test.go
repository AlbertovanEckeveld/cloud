package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/health"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
	"github.com/AlbertovanEckeveld/cloud/internal/core/site"
)

type fakeHealth struct{ report health.Report }

func (f fakeHealth) Report(context.Context) health.Report           { return f.report }
func (fakeHealth) Liveness(w http.ResponseWriter, _ *http.Request)  { w.WriteHeader(http.StatusOK) }
func (fakeHealth) Readiness(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

type fakeSites struct {
	sites []site.Site
	err   error
}

func (f fakeSites) List(context.Context) ([]site.Site, error) { return f.sites, f.err }

func testRouter(t *testing.T, sites fakeSites) http.Handler {
	t.Helper()
	spa, err := httpserver.NewSPA(fstest.MapFS{"index.html": {Data: []byte("<html><script>boot()</script></html>")}})
	if err != nil {
		t.Fatal(err)
	}
	return newRouter(routerDeps{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		siteID:    "home",
		version:   "v1.2.3",
		startedAt: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
		health: fakeHealth{health.Report{
			OK:     false,
			Checks: []health.Result{{Name: "database", OK: true, LatencyMS: 2}, {Name: "storage", OK: false, LatencyMS: 2000}},
		}},
		sites:    sites,
		frontend: spa,
		oidc:     http.NotFoundHandler(),
		avatars:  http.NotFound,
		drive:    func(chi.Router) {},
		identity: func(r chi.Router) {
			r.Post("/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		},
		requireUser: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-User") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		requireAdmin: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test-User") != "admin" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
	})
}

// get requests target as an admin.
func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Test-User", "admin")
	h.ServeHTTP(rec, req)
	return rec
}

func TestSystemEndpointsRequireAdmin(t *testing.T) {
	h := testRouter(t, fakeSites{})
	for _, path := range []string{"/api/v1/status", "/api/v1/sites"} {
		for user, want := range map[string]int{"": http.StatusUnauthorized, "user": http.StatusForbidden, "admin": http.StatusOK} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if user != "" {
				req.Header.Set("X-Test-User", user)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Errorf("%s as %q: status = %d, want %d", path, user, rec.Code, want)
			}
		}
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	h := testRouter(t, fakeSites{})
	for fetchSite, want := range map[string]int{"same-origin": http.StatusNoContent, "cross-site": http.StatusForbidden, "same-site": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.Header.Set("Sec-Fetch-Site", fetchSite)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Sec-Fetch-Site %s: status = %d, want %d", fetchSite, rec.Code, want)
		}
	}
}

func TestStatusEndpoint(t *testing.T) {
	rec := get(testRouter(t, fakeSites{}), "/api/v1/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("API responses must not be cached")
	}
	if rec.Header().Get("X-Request-ID") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("global middleware not applied")
	}
	var body statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SiteID != "home" || body.Version != "v1.2.3" || body.OK || len(body.Checks) != 2 || body.Checks[1].OK {
		t.Errorf("body = %+v", body)
	}
}

func TestSitesEndpoint(t *testing.T) {
	rec := get(testRouter(t, fakeSites{sites: []site.Site{{ID: "home", Version: "v1", Online: true}}}), "/api/v1/sites")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"home"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSitesEndpointHidesErrors(t *testing.T) {
	rec := get(testRouter(t, fakeSites{err: errors.New("dial tcp 10.0.0.5:5432: connection refused")}), "/api/v1/sites")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("internal error leaked: %s", rec.Body.String())
	}
}

func TestEmptySitesIsJSONArray(t *testing.T) {
	rec := get(testRouter(t, fakeSites{sites: []site.Site{}}), "/api/v1/sites")
	if !strings.Contains(rec.Body.String(), `"sites":[]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUnknownAPIRouteIsJSON404(t *testing.T) {
	rec := get(testRouter(t, fakeSites{}), "/api/v1/does-not-exist")
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d type=%s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(t, fakeSites{}).ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestFrontendFallbackHasPageCSP(t *testing.T) {
	rec := get(testRouter(t, fakeSites{}), "/some/client/route")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "boot()") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self' 'sha256-") {
		t.Errorf("page CSP not applied: %s", csp)
	}
}
