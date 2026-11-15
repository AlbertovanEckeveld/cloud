package database

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationLockID is the PostgreSQL advisory lock key held while migrating.
// Any int64 works as long as nothing else uses the same key; this one is the
// ASCII bytes of "platform".
const migrationLockID int64 = 0x706c6174666f726d

// moduleName restricts module (= schema) names to safe SQL identifiers.
var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// Module is a set of SQL migrations owned by one platform module. Each module
// lives in its own PostgreSQL schema with the same name, and tracks its applied
// migrations in <schema>.goose_db_version.
type Module struct {
	Name string
	FS   fs.FS
}

// Migrate applies all pending migrations of the given modules, in order.
//
// The whole run holds a session-level advisory lock, so when several instances
// start at once exactly one migrates and the others wait, then find nothing to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, modules ...Module) error {
	for _, m := range modules {
		if !moduleName.MatchString(m.Name) {
			return fmt.Errorf("invalid module name %q", m.Name)
		}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration lock: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Use a fresh context: the lock must be released even if ctx was cancelled.
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			logger.Error("release migration lock", "error", err)
		}
	}()

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	for _, m := range modules {
		schema := pgx.Identifier{m.Name}.Sanitize()
		if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema); err != nil {
			return fmt.Errorf("create schema %s: %w", m.Name, err)
		}

		provider, err := goose.NewProvider(goose.DialectPostgres, db, m.FS,
			goose.WithTableName(m.Name+".goose_db_version"),
			goose.WithDisableGlobalRegistry(true),
		)
		if err != nil {
			return fmt.Errorf("load migrations for %s: %w", m.Name, err)
		}

		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", m.Name, err)
		}
		for _, r := range results {
			logger.Info("migration applied", "module", m.Name, "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
		}
	}
	return nil
}
