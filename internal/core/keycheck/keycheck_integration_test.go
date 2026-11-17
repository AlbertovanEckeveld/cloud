//go:build integration

package keycheck

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

func newSealer(t *testing.T) *sealer.Sealer {
	t.Helper()
	key := make([]byte, 32)
	rand.Read(key)
	s, err := sealer.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerify(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fsys, err := migrations.For("core")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil)),
		database.Module{Name: "core", FS: fsys}); err != nil {
		t.Fatal(err)
	}

	// The canary is a single row that other test packages' platforms also
	// use: work in a transaction that is rolled back, so their row (if any)
	// is untouched.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `DELETE FROM core.key_canary`); err != nil {
		t.Fatal(err)
	}

	key := newSealer(t)
	if err := Verify(ctx, tx, key); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := Verify(ctx, tx, key); err != nil {
		t.Fatalf("same key again: %v", err)
	}
	if err := Verify(ctx, tx, newSealer(t)); !errors.Is(err, ErrMismatch) {
		t.Fatalf("other key: got %v, want ErrMismatch", err)
	}
	// The wrong key must not have replaced the canary.
	if err := Verify(ctx, tx, key); err != nil {
		t.Fatalf("original key after a wrong one: %v", err)
	}
}
