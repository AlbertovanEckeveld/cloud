// Package health runs dependency checks and serves liveness and readiness probes.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Check is a named dependency check. It must respect ctx cancellation.
type Check struct {
	Name string
	Run  func(ctx context.Context) error
}

// Result is the outcome of one check. Error details are deliberately not part
// of it: they are logged, but never shown to clients.
type Result struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
}

// Report is the outcome of all checks at one moment.
type Report struct {
	OK        bool      `json:"ok"`
	Checks    []Result  `json:"checks"`
	CheckedAt time.Time `json:"checked_at"`
}

// Checker runs checks concurrently and caches the report for a short time, so
// that probe traffic cannot be amplified into load on the dependencies.
type Checker struct {
	checks   []Check
	timeout  time.Duration
	cacheTTL time.Duration
	logger   *slog.Logger
	now      func() time.Time

	draining atomic.Bool

	mu   sync.Mutex
	last Report
}

// NewChecker returns a checker with a 2s per-check timeout and a 2s cache.
func NewChecker(logger *slog.Logger, checks ...Check) *Checker {
	return &Checker{
		checks:   checks,
		timeout:  2 * time.Second,
		cacheTTL: 2 * time.Second,
		logger:   logger,
		now:      time.Now,
	}
}

// Drain marks the instance as shutting down: readiness fails from now on, so a
// load balancer stops sending new requests while in-flight ones finish.
func (c *Checker) Drain() { c.draining.Store(true) }

// Report returns the current health, running the checks if the cached report expired.
func (c *Checker) Report(ctx context.Context) Report {
	// Holding the lock while checking means concurrent callers wait for one
	// run and then share its result, instead of each hitting the dependencies.
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.last.CheckedAt.IsZero() && c.now().Sub(c.last.CheckedAt) < c.cacheTTL {
		return c.last
	}

	// A check result is shared with other callers, so it must not be cut short
	// because this particular caller hung up.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.timeout)
	defer cancel()

	results := make([]Result, len(c.checks))
	var wg sync.WaitGroup
	for i, check := range c.checks {
		wg.Go(func() {
			start := c.now()
			err := check.Run(ctx)
			results[i] = Result{Name: check.Name, OK: err == nil, LatencyMS: c.now().Sub(start).Milliseconds()}
			if err != nil {
				c.logger.Warn("health check failed", "check", check.Name, "error", err)
			}
		})
	}
	wg.Wait()

	report := Report{OK: true, Checks: results, CheckedAt: c.now().UTC()}
	for _, r := range results {
		report.OK = report.OK && r.OK
	}
	c.last = report
	return report
}

// Liveness reports whether the process is running. It never checks
// dependencies: restarting the process would not fix a database outage.
func (c *Checker) Liveness(w http.ResponseWriter, _ *http.Request) {
	writeProbe(w, http.StatusOK, "ok")
}

// Readiness reports whether this instance can serve traffic.
func (c *Checker) Readiness(w http.ResponseWriter, r *http.Request) {
	if c.draining.Load() {
		writeProbe(w, http.StatusServiceUnavailable, "draining")
		return
	}
	if !c.Report(r.Context()).OK {
		writeProbe(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	writeProbe(w, http.StatusOK, "ready")
}

func writeProbe(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
