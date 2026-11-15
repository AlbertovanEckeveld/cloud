package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Audit event names. They are stable identifiers: the frontend translates them.
const (
	EventUserCreated       = "user.created"
	EventActivationIssued  = "auth.activation.issued"
	EventActivationStarted = "auth.activation.started"
	EventTOTPEnrolled      = "auth.totp.enrolled"
	EventLoginSucceeded    = "auth.login.succeeded"
	EventLoginFailed       = "auth.login.failed"
	EventTOTPFailed        = "auth.totp.failed"
	EventLogout            = "auth.logout"
	EventPasswordChanged   = "auth.password.changed"

	EventPasskeyAdded           = "auth.passkey.added"
	EventPasskeyRemoved         = "auth.passkey.removed"
	EventPasskeyFailed          = "auth.passkey.failed"
	EventRecoveryCodesGenerated = "auth.recovery_codes.generated"
	EventRecoveryCodeUsed       = "auth.recovery_code.used"
	EventSessionRevoked         = "auth.session.revoked"
)

// auditEntry is one event to record. actor is who did it, subject whom it concerns.
type auditEntry struct {
	event   string
	actor   *uuid.UUID
	subject *uuid.UUID
	details map[string]any
}

// AuditEvent is a recorded event as shown to users.
type AuditEvent struct {
	ID         uuid.UUID `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Event      string    `json:"event"`
	SiteID     string    `json:"site_id"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
}

func (s *Service) audit(ctx context.Context, q dbtx, c Client, e auditEntry) error {
	details := e.details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	_, err = q.Exec(ctx, `
		INSERT INTO identity.audit_events (id, occurred_at, site_id, event, actor_id, subject_id, ip, user_agent, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		newID(), s.now(), s.opts.SiteID, e.event, e.actor, e.subject, c.IP, c.UserAgent, raw)
	if err != nil {
		return fmt.Errorf("write audit event %s: %w", e.event, err)
	}
	return nil
}

// auditBestEffort records an event outside a transaction, typically a
// failure. If recording fails, the original outcome still stands.
func (s *Service) auditBestEffort(ctx context.Context, c Client, e auditEntry) {
	if err := s.audit(ctx, s.db, c, e); err != nil {
		s.log.Error("audit event lost", "event", e.event, "error", err)
	}
}

// Activity returns the most recent events done by or concerning a user.
func (s *Service) Activity(ctx context.Context, userID uuid.UUID, limit int) ([]AuditEvent, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, occurred_at, event, site_id, ip, user_agent
		FROM identity.audit_events
		WHERE actor_id = $1 OR subject_id = $1
		ORDER BY occurred_at DESC, id DESC -- UUIDv7 is time-ordered: a stable tiebreak
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query activity: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEvent, error) {
		var e AuditEvent
		err := row.Scan(&e.ID, &e.OccurredAt, &e.Event, &e.SiteID, &e.IP, &e.UserAgent)
		e.OccurredAt = e.OccurredAt.UTC()
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("read activity: %w", err)
	}
	return events, nil
}
