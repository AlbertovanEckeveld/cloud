//go:build integration

package database

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

func testConfig(t *testing.T) config.Database {
	t.Helper()
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	return config.Database{URL: config.Secret(u)}
}

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := Open(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testModule returns a uniquely named throwaway module and drops its schema afterwards.
func testModule(t *testing.T, pool *pgxpool.Pool) Module {
	t.Helper()
	name := fmt.Sprintf("it_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+name+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	return Module{Name: name, FS: fstest.MapFS{
		"00001_items.sql": {Data: []byte("-- +goose Up\nCREATE TABLE " + name + ".items (id uuid PRIMARY KEY);\n-- +goose Down\nDROP TABLE " + name + ".items;\n")},
		"00002_name.sql":  {Data: []byte("-- +goose Up\nALTER TABLE " + name + ".items ADD COLUMN name text;\n-- +goose Down\nALTER TABLE " + name + ".items DROP COLUMN name;\n")},
	}}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOpenRejectsWrongPasswordWithoutLeakingIt(t *testing.T) {
	cfg := testConfig(t)
	u, err := url.Parse(cfg.URL.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	const wrong = "definitely-wrong-password"
	u.User = url.UserPassword(u.User.Username(), wrong)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = Open(ctx, config.Database{URL: config.Secret(u.String())})
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if strings.Contains(err.Error(), wrong) {
		t.Fatalf("error leaks password: %v", err)
	}
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	pool := openTestPool(t)
	m := testModule(t, pool)
	ctx := context.Background()

	if err := Migrate(ctx, pool, quietLogger(), m); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(ctx, pool, quietLogger(), m); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var version int64
	err := pool.QueryRow(ctx, "SELECT max(version_id) FROM "+m.Name+".goose_db_version").Scan(&version)
	if err != nil {
		t.Fatalf("read version table: %v", err)
	}
	if version != 2 {
		t.Errorf("version = %d, want 2", version)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+m.Name+".items (id, name) VALUES (gen_random_uuid(), 'x')"); err != nil {
		t.Errorf("migrated table unusable: %v", err)
	}
}

func TestMigrateConcurrentInstances(t *testing.T) {
	pool := openTestPool(t)
	m := testModule(t, pool)

	// Simulate several platform instances starting at the same moment; each
	// instance has its own connection pool, like separate processes would.
	pools := make([]*pgxpool.Pool, 4)
	for i := range pools {
		pools[i] = openTestPool(t)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(pools))
	for _, p := range pools {
		wg.Go(func() { errs <- Migrate(context.Background(), p, quietLogger(), m) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Migrate: %v", err)
		}
	}
}

func TestMigrateRejectsUnsafeModuleName(t *testing.T) {
	pool := openTestPool(t)
	err := Migrate(context.Background(), pool, quietLogger(), Module{Name: `core"; DROP SCHEMA core; --`, FS: fstest.MapFS{}})
	if err == nil || !strings.Contains(err.Error(), "invalid module name") {
		t.Fatalf("err = %v", err)
	}
}

func TestMigrateCoreModule(t *testing.T) {
	pool := openTestPool(t)
	fsys, err := migrations.For("core")
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), pool, quietLogger(), Module{Name: "core", FS: fsys}); err != nil {
		t.Fatalf("Migrate core: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(context.Background(), "SELECT to_regclass('core.sites') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatalf("core.sites missing (err=%v)", err)
	}
}
