// Package httpserver contains the HTTP server, shared middleware and response helpers.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 15 * time.Second

// Run serves h on ln until ctx is cancelled, then shuts down gracefully:
// onShutdown is called first (to fail readiness), then in-flight requests get
// up to 15 seconds to finish.
func Run(ctx context.Context, logger *slog.Logger, ln net.Listener, h http.Handler, onShutdown func()) error {
	srv := &http.Server{
		Handler: h,
		// Timeouts protect against slow clients holding connections open
		// (slowloris). WriteTimeout will need per-route overrides for large
		// downloads and uploads in later phases.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", ln.Addr().String())
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down http server")
	onShutdown()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server shutdown: %w", err)
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
