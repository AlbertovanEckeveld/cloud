// Package site registers this platform instance in core.sites and keeps its
// heartbeat fresh, so every instance can see which sites are alive.
package site

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HeartbeatInterval is how often a running instance refreshes last_seen_at.
const HeartbeatInterval = 30 * time.Second

// offlineAfter is how long a site may miss heartbeats before it counts as offline.
const offlineAfter = 3 * HeartbeatInterval

// Site is one platform site as seen in the registry.
type Site struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Online      bool      `json:"online"`
}

// Registry reads and writes core.sites.
type Registry struct {
	pool    *pgxpool.Pool
	id      string
	version string
	logger  *slog.Logger
}

// NewRegistry returns a registry for the site this instance belongs to.
func NewRegistry(pool *pgxpool.Pool, id, version string, logger *slog.Logger) *Registry {
	return &Registry{pool: pool, id: id, version: version, logger: logger}
}

// Heartbeat records that this site is alive and which version it runs.
func (r *Registry) Heartbeat(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO core.sites (id, version) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, last_seen_at = now()`,
		r.id, r.version)
	if err != nil {
		return fmt.Errorf("site heartbeat: %w", err)
	}
	return nil
}

// Run sends a heartbeat every HeartbeatInterval until ctx is cancelled.
// Failures are logged and retried on the next tick; they never stop the loop.
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Heartbeat(ctx); err != nil && ctx.Err() == nil {
				r.logger.Warn("site heartbeat failed", "error", err)
			}
		}
	}
}

// List returns all known sites, ordered by ID. Online is computed with the
// database clock, so clock skew between sites does not affect it.
func (r *Registry) List(ctx context.Context) ([]Site, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, version, first_seen_at, last_seen_at, last_seen_at > now() - $1::interval
		FROM core.sites ORDER BY id`,
		offlineAfter)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	sites, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Site, error) {
		var s Site
		err := row.Scan(&s.ID, &s.Version, &s.FirstSeenAt, &s.LastSeenAt, &s.Online)
		// pgx returns timestamptz in the process's local zone; the API speaks UTC.
		s.FirstSeenAt, s.LastSeenAt = s.FirstSeenAt.UTC(), s.LastSeenAt.UTC()
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	return sites, nil
}
