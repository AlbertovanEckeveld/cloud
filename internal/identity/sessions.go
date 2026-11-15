package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SessionInfo describes one signed-in device for the session overview.
type SessionInfo struct {
	ID         uuid.UUID `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	SiteID     string    `json:"site_id"`
	Current    bool      `json:"current"`
}

// ListSessions returns the user's active full sessions, most recently used first.
func (s *Service) ListSessions(ctx context.Context, sess Session) ([]SessionInfo, error) {
	now := s.now()
	rows, err := s.db.Query(ctx, `
		SELECT id, created_at, last_seen_at, ip, user_agent, site_id
		FROM identity.sessions
		WHERE user_id = $1 AND state = 'authenticated' AND expires_at > $2 AND last_seen_at > $3
		ORDER BY last_seen_at DESC, id DESC`, sess.UserID, now, now.Add(-sessionIdleTTL))
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SessionInfo, error) {
		var i SessionInfo
		err := row.Scan(&i.ID, &i.CreatedAt, &i.LastSeenAt, &i.IP, &i.UserAgent, &i.SiteID)
		i.CreatedAt, i.LastSeenAt = i.CreatedAt.UTC(), i.LastSeenAt.UTC()
		i.Current = i.ID == sess.ID
		return i, err
	})
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	return sessions, nil
}

// RevokeSession signs out one of the user's sessions (possibly the current one).
func (s *Service) RevokeSession(ctx context.Context, c Client, sess Session, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1 AND user_id = $2`, id, sess.UserID)
		if err != nil {
			return fmt.Errorf("revoke session: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventSessionRevoked, actor: &sess.UserID, details: map[string]any{"count": 1}})
	})
}

// RevokeOtherSessions signs out every session of the user except the current one.
func (s *Service) RevokeOtherSessions(ctx context.Context, c Client, sess Session) (int, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1 AND id <> $2`, sess.UserID, sess.ID)
		if err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		n = tag.RowsAffected()
		if n == 0 {
			return nil
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventSessionRevoked, actor: &sess.UserID, details: map[string]any{"count": n}})
	})
	return int(n), err
}
