// Package assistant searches by meaning and answers questions about the
// files a user can open, with an AI model the administrator configures in
// the web app (ADR 0023). It keeps its own index of embeddings; the text
// stays in drive, and every result passes drive's permission check before
// it reaches a page or a model.
package assistant

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
	"github.com/AlbertovanEckeveld/cloud/internal/drive"
)

// Files is what the assistant needs from drive. Text and TextIndex check
// no permissions and are only for indexing; Visible and Search do.
type Files interface {
	TextIndex(ctx context.Context, after uuid.UUID, limit int) ([]drive.TextRef, error)
	TextCount(ctx context.Context) (int, error)
	Text(ctx context.Context, nodeID uuid.UUID) (drive.TextRef, string, error)
	Visible(ctx context.Context, userID uuid.UUID, ids []uuid.UUID) ([]drive.SearchResult, error)
	Search(ctx context.Context, userID uuid.UUID, query string) ([]drive.SearchResult, error)
}

// Options configure the service.
type Options struct {
	Logger *slog.Logger
	// Services are the platform's own backends (host:port): Postgres, S3.
	// No provider may point at them.
	Services []string
}

// Service is the assistant.
type Service struct {
	db     *pgxpool.Pool
	sealer *sealer.Sealer
	source Files
	log    *slog.Logger
	guard  *guard
	now    func() time.Time
	kick   chan struct{}

	userID func(*http.Request) uuid.UUID
	audit  func(r *http.Request, event string, details map[string]any)
}

// New returns the assistant service.
func New(db *pgxpool.Pool, s *sealer.Sealer, source Files, opts Options) *Service {
	return &Service{
		db: db, sealer: s, source: source, log: opts.Logger,
		guard: &guard{services: opts.Services, resolver: net.DefaultResolver},
		now:   time.Now, kick: make(chan struct{}, 1),
	}
}

// Errors.
var (
	ErrNotFound      = errors.New("assistant: not found")
	ErrNotConfigured = errors.New("assistant: not configured")
)

// InputError is a validation error whose message is safe to show.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// Run keeps the index in step with drive and removes old conversations,
// until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		if err := s.sync(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("assistant index", "error", err)
		}
		if s.now().Sub(lastPrune) > time.Hour {
			if err := s.pruneConversations(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("assistant prune conversations", "error", err)
			}
			lastPrune = s.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.kick:
		}
	}
}

// Kick starts an index round soon, for example after the model changed.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
