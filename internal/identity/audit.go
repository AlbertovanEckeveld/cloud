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

	EventUserUpdated        = "user.updated"
	EventUserDisabled       = "user.disabled"
	EventUserEnabled        = "user.enabled"
	EventGroupCreated       = "group.created"
	EventGroupUpdated       = "group.updated"
	EventGroupDeleted       = "group.deleted"
	EventGroupMemberAdded   = "group.member_added"
	EventGroupMemberRemoved = "group.member_removed"

	EventProfileUpdated   = "user.profile_updated"
	EventLogoutEverywhere = "auth.logout_everywhere"
	EventAppRevoked       = "oidc.app_revoked"

	EventOIDCLogin               = "oidc.login"
	EventOIDCDenied              = "oidc.denied"
	EventOIDCClientCreated       = "oidc.client.created"
	EventOIDCClientUpdated       = "oidc.client.updated"
	EventOIDCClientDeleted       = "oidc.client.deleted"
	EventOIDCClientSecretRotated = "oidc.client.secret_rotated"
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

// AuditEntry is a recorded event as shown to administrators: with the
// people involved and the details.
type AuditEntry struct {
	AuditEvent
	Actor   *string        `json:"actor"`   // username, if any
	Subject *string        `json:"subject"` // username, if any
	Details map[string]any `json:"details"`
}

// ListAudit returns events across all users, newest first, at most limit.
// Pass the ID of the last event of the previous page as before to continue;
// UUIDv7 IDs are ordered by creation time.
func (s *Service) ListAudit(ctx context.Context, before *uuid.UUID, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT e.id, e.occurred_at, e.event, e.site_id, e.ip, e.user_agent, a.username, sub.username, e.details
		FROM identity.audit_events e
		LEFT JOIN identity.users a ON a.id = e.actor_id
		LEFT JOIN identity.users sub ON sub.id = e.subject_id
		WHERE $1::uuid IS NULL OR e.id < $1
		ORDER BY e.id DESC
		LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := row.Scan(&e.ID, &e.OccurredAt, &e.Event, &e.SiteID, &e.IP, &e.UserAgent, &e.Actor, &e.Subject, &e.Details)
		e.OccurredAt = e.OccurredAt.UTC()
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return entries, nil
}
