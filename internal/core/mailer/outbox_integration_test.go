//go:build integration

package mailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/config"
	"github.com/AlbertovanEckeveld/cloud/internal/core/database"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/migrations"
)

type sent struct{ to, raw string }

// testMailer has a fake transport and a recipient unique to the test.
type testMailer struct {
	*Mailer
	pool      *pgxpool.Pool
	recipient string
	mu        sync.Mutex
	sent      []sent
	fail      error
	clock     time.Time
}

func newTestMailer(t *testing.T) *testMailer {
	t.Helper()
	ctx := context.Background()
	pool, err := database.Open(ctx, config.Database{URL: config.Secret(os.Getenv("DATABASE_URL"))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fsys, _ := migrations.For("core")
	if err := database.Migrate(ctx, pool, logger, database.Module{Name: "core", FS: fsys}); err != nil {
		t.Fatal(err)
	}
	sl, _ := sealer.New(bytes.Repeat([]byte{9}, 32))
	m, err := New(pool, sl, &config.Mail{From: "Cloud <cloud@example.test>"}, "test", logger)
	if err != nil {
		t.Fatal(err)
	}
	tm := &testMailer{Mailer: m, pool: pool, clock: time.Now(),
		recipient: fmt.Sprintf("it-%d@example.test", time.Now().UnixNano())}
	m.now = func() time.Time { return tm.clock }
	m.send = func(_ context.Context, _, to string, raw []byte) error {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		if tm.fail != nil {
			return tm.fail
		}
		tm.sent = append(tm.sent, sent{to, string(raw)})
		return nil
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM core.mail_outbox WHERE recipient = $1`, tm.recipient); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return tm
}

func (tm *testMailer) queued(t *testing.T) (count, attempts int) {
	t.Helper()
	err := tm.pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(max(attempts), 0) FROM core.mail_outbox WHERE recipient = $1`, tm.recipient).Scan(&count, &attempts)
	if err != nil {
		t.Fatal(err)
	}
	return count, attempts
}

func TestEnqueueIsTransactional(t *testing.T) {
	tm := newTestMailer(t)
	ctx := context.Background()
	msg := Message{To: tm.recipient, Subject: "Test", Body: "geheim: https://x/activate#token"}

	errRollback := errors.New("rollback")
	err := pgx.BeginFunc(ctx, tm.pool, func(tx pgx.Tx) error {
		if err := tm.Enqueue(ctx, tx, msg); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	if n, _ := tm.queued(t); n != 0 {
		t.Fatal("mail queued although the transaction rolled back")
	}

	if err := tm.Enqueue(ctx, tm.pool, msg); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := tm.pool.QueryRow(ctx, `SELECT body_sealed FROM core.mail_outbox WHERE recipient = $1`, tm.recipient).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("activate#token")) {
		t.Fatal("mail body stored in plaintext")
	}
}

func TestDispatchDeliversAndDeletes(t *testing.T) {
	tm := newTestMailer(t)
	ctx := context.Background()
	if err := tm.Enqueue(ctx, tm.pool, Message{To: tm.recipient, Subject: "Hallo", Body: "inhoud\n"}); err != nil {
		t.Fatal(err)
	}
	if err := tm.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	var mine []sent
	for _, s := range tm.sent {
		if s.to == tm.recipient {
			mine = append(mine, s)
		}
	}
	if len(mine) != 1 || !strings.Contains(mine[0].raw, "Subject: Hallo") || !strings.Contains(mine[0].raw, "inhoud") {
		t.Fatalf("sent = %+v", mine)
	}
	if n, _ := tm.queued(t); n != 0 {
		t.Error("delivered mail still queued")
	}
}

func TestDispatchRetriesWithBackoffThenGivesUp(t *testing.T) {
	tm := newTestMailer(t)
	ctx := context.Background()
	tm.fail = errors.New("421 try again later")
	if err := tm.Enqueue(ctx, tm.pool, Message{To: tm.recipient, Subject: "Retry", Body: "x"}); err != nil {
		t.Fatal(err)
	}

	if err := tm.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if n, attempts := tm.queued(t); n != 1 || attempts != 1 {
		t.Fatalf("after first failure: %d queued, %d attempts", n, attempts)
	}
	// Not due yet: a second dispatch right away does not retry.
	if err := tm.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, attempts := tm.queued(t); attempts != 1 {
		t.Fatalf("retried before backoff: %d attempts", attempts)
	}

	for range maxAttempts {
		tm.clock = tm.clock.Add(5 * time.Hour)
		if err := tm.dispatch(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := tm.queued(t); n != 0 {
		t.Error("mail not dropped after max attempts")
	}
}
