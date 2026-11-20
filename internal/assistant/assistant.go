// Package assistant searches by meaning and answers questions about what a
// user can see (files now; notes, photos and mail later), with an AI model
// the administrator configures in the web app (ADR 0023, 0024). It keeps
// its own index of embeddings; the text stays with each module, reached
// through knowledge.Source, and every result passes that module's
// permission check before it reaches a page or a model.
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

	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
)

// Options configure the service.
type Options struct {
	Logger *slog.Logger
	// Services are the platform's own backends (host:port): Postgres, S3.
	// No provider may point at them.
	Services []string
}

// Service is the assistant.
type Service struct {
	db      *pgxpool.Pool
	sealer  *sealer.Sealer
	sources []knowledge.Source
	byName  map[string]knowledge.Source
	log     *slog.Logger
	guard   *guard
	now     func() time.Time
	kick    chan struct{}

	userID func(*http.Request) uuid.UUID
	audit  func(r *http.Request, event string, details map[string]any)
}

// New returns the assistant service over the given sources, in the order
// their results are preferred when equally good.
func New(db *pgxpool.Pool, s *sealer.Sealer, sources []knowledge.Source, opts Options) *Service {
	byName := map[string]knowledge.Source{}
	for _, src := range sources {
		byName[src.Name()] = src
	}
	return &Service{
		db: db, sealer: s, sources: sources, byName: byName, log: opts.Logger,
		guard: &guard{services: opts.Services, resolver: net.DefaultResolver},
		now:   time.Now, kick: make(chan struct{}, 1),
	}
}

// sourceNames lists the registered sources.
func (s *Service) sourceNames() []string {
	names := make([]string, len(s.sources))
	for i, src := range s.sources {
		names[i] = src.Name()
	}
	return names
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
