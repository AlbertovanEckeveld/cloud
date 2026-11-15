// Package database opens the PostgreSQL connection pool and runs migrations.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
)

// Open creates a connection pool and verifies that the database is reachable.
func Open(ctx context.Context, cfg config.Database) (*pgxpool.Pool, error) {
	// pgx redacts the password in its own parse errors.
	pc, err := pgxpool.ParseConfig(cfg.URL.Reveal())
	if err != nil {
		return nil, fmt.Errorf("parse database config: %w", err)
	}
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.MaxConnLifetime = time.Hour
	pc.MaxConnIdleTime = 15 * time.Minute
	// Every connection is tagged, so the source is visible in pg_stat_activity.
	pc.ConnConfig.RuntimeParams["application_name"] = "platform"
	// Return timestamps in UTC regardless of the server's timezone setting.
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database %s:%d: %w", pc.ConnConfig.Host, pc.ConnConfig.Port, err)
	}
	return pool, nil
}
