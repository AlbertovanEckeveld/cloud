//go:build integration

package site

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

func TestHeartbeatAndList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fsys, err := migrations.For("core")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, logger, database.Module{Name: "core", FS: fsys}); err != nil {
		t.Fatal(err)
	}

	id := fmt.Sprintf("it-%d", time.Now().UnixNano()%1e9)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM core.sites WHERE id LIKE 'it-%'"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	reg := NewRegistry(pool, id, "v1", logger)
	if err := reg.Heartbeat(ctx); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	// A second heartbeat with a new version updates the row instead of failing.
	if err := NewRegistry(pool, id, "v2", logger).Heartbeat(ctx); err != nil {
		t.Fatalf("second Heartbeat: %v", err)
	}

	// A site that stopped sending heartbeats long ago.
	stale := id + "-stale"
	if _, err := pool.Exec(ctx, "INSERT INTO core.sites (id, version, last_seen_at) VALUES ($1, 'v0', now() - interval '1 hour')", stale); err != nil {
		t.Fatal(err)
	}

	sites, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]Site{}
	for _, s := range sites {
		got[s.ID] = s
	}
	if s := got[id]; !s.Online || s.Version != "v2" || s.FirstSeenAt.After(s.LastSeenAt) {
		t.Errorf("live site = %+v", s)
	}
	if s, ok := got[stale]; !ok || s.Online {
		t.Errorf("stale site = %+v (present=%v), want offline", s, ok)
	}
}

func TestDatabaseRejectsInvalidSiteID(t *testing.T) {
	ctx := context.Background()
	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The CHECK constraint is a second line of defence behind config validation.
	err = NewRegistry(pool, "Bad Site!", "v1", slog.New(slog.NewTextHandler(io.Discard, nil))).Heartbeat(ctx)
	if err == nil {
		t.Fatal("expected CHECK constraint violation")
	}
}
