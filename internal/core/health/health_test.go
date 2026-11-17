package health

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ok(context.Context) error { return nil }

func TestReportAllHealthy(t *testing.T) {
	c := NewChecker(discard(), Check{Name: "database", Run: ok}, Check{Name: "storage", Run: ok})
	r := c.Report(context.Background())
	if !r.OK || len(r.Checks) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if r.Checks[0].Name != "database" || r.Checks[1].Name != "storage" {
		t.Errorf("checks out of order: %+v", r.Checks)
	}
}

func TestReportFailureIsLoggedNotExposed(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	c := NewChecker(logger,
		Check{Name: "database", Run: ok},
		Check{Name: "storage", Run: func(context.Context) error { return errors.New("dial tcp 10.0.0.5:9000: refused") }},
	)
	r := c.Report(context.Background())
	if r.OK || r.Checks[0].OK != true || r.Checks[1].OK != false {
		t.Fatalf("report = %+v", r)
	}
	if !strings.Contains(logs.String(), "10.0.0.5") {
		t.Error("failure details should be logged for the operator")
	}
}

func TestOptionalCheckDoesNotFailReadiness(t *testing.T) {
	failing := func(context.Context) error { return errors.New("editor unreachable") }
	c := NewChecker(discard(), Check{Name: "database", Run: ok}, Check{Name: "collabora", Run: failing, Optional: true})
	r := c.Report(context.Background())
	if !r.OK || r.Checks[1].OK || !r.Checks[1].Optional {
		t.Fatalf("report = %+v", r)
	}
	w := httptest.NewRecorder()
	c.Readiness(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Errorf("readiness = %d, want 200", w.Code)
	}
}

func TestReportCachesResults(t *testing.T) {
	var calls atomic.Int32
	c := NewChecker(discard(), Check{Name: "db", Run: func(context.Context) error { calls.Add(1); return nil }})
	now := time.Now()
	c.now = func() time.Time { return now }

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { c.Report(context.Background()) })
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("check ran %d times, want 1 (cached)", got)
	}

	now = now.Add(3 * time.Second)
	c.Report(context.Background())
	if got := calls.Load(); got != 2 {
		t.Fatalf("check ran %d times after TTL, want 2", got)
	}
}

func TestReportTimesOutHangingCheck(t *testing.T) {
	c := NewChecker(discard(), Check{Name: "hang", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }})
	c.timeout = 50 * time.Millisecond

	start := time.Now()
	r := c.Report(context.Background())
	if r.OK {
		t.Fatal("hanging check reported healthy")
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestReportIgnoresCallerCancellation(t *testing.T) {
	c := NewChecker(discard(), Check{Name: "db", Run: func(ctx context.Context) error { return ctx.Err() }})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !c.Report(ctx).OK {
		t.Fatal("a cancelled caller must not poison the shared report")
	}
}

func TestProbes(t *testing.T) {
	healthy := true
	c := NewChecker(discard(), Check{Name: "db", Run: func(context.Context) error {
		if healthy {
			return nil
		}
		return errors.New("down")
	}})
	c.cacheTTL = 0

	probe := func(h http.HandlerFunc) int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Code
	}

	if got := probe(c.Readiness); got != http.StatusOK {
		t.Errorf("ready healthy = %d", got)
	}
	healthy = false
	if got := probe(c.Readiness); got != http.StatusServiceUnavailable {
		t.Errorf("ready unhealthy = %d", got)
	}
	if got := probe(c.Liveness); got != http.StatusOK {
		t.Errorf("liveness must not depend on dependencies, got %d", got)
	}

	healthy = true
	c.Drain()
	if got := probe(c.Readiness); got != http.StatusServiceUnavailable {
		t.Errorf("ready while draining = %d", got)
	}
}
