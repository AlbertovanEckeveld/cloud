package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

var _ authz.Authorizer = (*Service)(nil)

// Permissions returns the strongest permission per resource that the user
// holds directly or through a group. Group membership is resolved on every
// call, so leaving a group takes effect at once; disabled users hold nothing.
func (s *Service) Permissions(ctx context.Context, userID uuid.UUID, resourceType string, ids []uuid.UUID) (map[uuid.UUID]authz.Permission, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.resource_id, g.permission FROM identity.grants g
		WHERE g.resource_type = $2 AND g.resource_id = ANY($3)
		  AND EXISTS (SELECT 1 FROM identity.users u WHERE u.id = $1 AND u.disabled_at IS NULL)
		  AND ((g.subject_type = 'user' AND g.subject_id = $1) OR
		       (g.subject_type = 'group' AND g.subject_id IN (SELECT group_id FROM identity.group_members WHERE user_id = $1)))`,
		userID, resourceType, ids)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]authz.Permission{}
	for rows.Next() {
		var id uuid.UUID
		var p authz.Permission
		if err := rows.Scan(&id, &p); err != nil {
			return nil, fmt.Errorf("read permissions: %w", err)
		}
		out[id] = authz.Max(out[id], p)
	}
	return out, rows.Err()
}

// Shared reports which resources have at least one share.
func (s *Service) Shared(ctx context.Context, resourceType string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT resource_id FROM identity.grants WHERE resource_type = $1 AND resource_id = ANY($2)`,
		resourceType, ids)
	if err != nil {
		return nil, fmt.Errorf("load shared: %w", err)
	}
	shared, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("read shared: %w", err)
	}
	out := make(map[uuid.UUID]bool, len(shared))
	for _, id := range shared {
		out[id] = true
	}
	return out, nil
}

// Grants lists the shares of a resource with the names of the people and groups.
func (s *Service) Grants(ctx context.Context, resourceType string, id uuid.UUID) ([]authz.Grant, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.subject_type, g.subject_id, COALESCE(u.display_name, gr.name, '?'), g.permission, g.granted_by, g.granted_at
		FROM identity.grants g
		LEFT JOIN identity.users u ON g.subject_type = 'user' AND u.id = g.subject_id
		LEFT JOIN identity.groups gr ON g.subject_type = 'group' AND gr.id = g.subject_id
		WHERE g.resource_type = $1 AND g.resource_id = $2
		ORDER BY g.subject_type DESC, 3`, resourceType, id)
	if err != nil {
		return nil, fmt.Errorf("list grants: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (authz.Grant, error) {
		var g authz.Grant
		err := row.Scan(&g.Subject.Type, &g.Subject.ID, &g.Name, &g.Permission, &g.GrantedBy, &g.GrantedAt)
		g.GrantedAt = g.GrantedAt.UTC()
		return g, err
	})
}

// Grant shares a resource with a user or group, or changes the level of an
// existing share. The caller (the owning module) has checked that the actor
// may share it.
func (s *Service) Grant(ctx context.Context, actor uuid.UUID, resourceType string, id uuid.UUID, to authz.Subject, p authz.Permission) error {
	if !p.Valid() {
		return authz.ErrInvalidPermission
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var exists bool
		var err error
		switch to.Type {
		case authz.User:
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE id = $1 AND disabled_at IS NULL)`, to.ID).Scan(&exists)
		case authz.Group:
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.groups WHERE id = $1)`, to.ID).Scan(&exists)
		}
		if err != nil {
			return fmt.Errorf("check share target: %w", err)
		}
		if !exists {
			return authz.ErrUnknownSubject
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.grants (resource_type, resource_id, subject_type, subject_id, permission, granted_by, granted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (resource_type, resource_id, subject_type, subject_id)
			DO UPDATE SET permission = EXCLUDED.permission, granted_by = EXCLUDED.granted_by, granted_at = EXCLUDED.granted_at`,
			resourceType, id, to.Type, to.ID, p, actor, s.now()); err != nil {
			return fmt.Errorf("store grant: %w", err)
		}
		return s.audit(ctx, tx, Client{}, shareAudit(EventShareGranted, actor, resourceType, id, to, p))
	})
}

// Revoke removes one share.
func (s *Service) Revoke(ctx context.Context, actor uuid.UUID, resourceType string, id uuid.UUID, from authz.Subject) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM identity.grants WHERE resource_type = $1 AND resource_id = $2 AND subject_type = $3 AND subject_id = $4`,
			resourceType, id, from.Type, from.ID)
		if err != nil {
			return fmt.Errorf("revoke grant: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return authz.ErrNoGrant
		}
		return s.audit(ctx, tx, Client{}, shareAudit(EventShareRevoked, actor, resourceType, id, from, authz.None))
	})
}

func shareAudit(event string, actor uuid.UUID, resourceType string, id uuid.UUID, subject authz.Subject, p authz.Permission) auditEntry {
	e := auditEntry{event: event, actor: &actor, details: map[string]any{
		"resource_type": resourceType, "resource_id": id.String(), "subject_type": string(subject.Type),
	}}
	if p != authz.None {
		e.details["permission"] = string(p)
	}
	if subject.Type == authz.User {
		e.subject = &subject.ID
	} else {
		e.details["group_id"] = subject.ID.String()
	}
	return e
}

// RevokeAll removes all shares of resources that were deleted.
func (s *Service) RevokeAll(ctx context.Context, resourceType string, ids []uuid.UUID) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM identity.grants WHERE resource_type = $1 AND resource_id = ANY($2)`, resourceType, ids); err != nil {
		return fmt.Errorf("revoke grants: %w", err)
	}
	return nil
}

// SharedWith lists the resources of a type shared with a user, directly or
// through a group, with the strongest permission for each.
func (s *Service) SharedWith(ctx context.Context, userID uuid.UUID, resourceType string) ([]authz.Shared, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT ON (g.resource_id) g.resource_id, g.permission, g.granted_by
		FROM identity.grants g
		WHERE g.resource_type = $2
		  AND EXISTS (SELECT 1 FROM identity.users u WHERE u.id = $1 AND u.disabled_at IS NULL)
		  AND ((g.subject_type = 'user' AND g.subject_id = $1) OR
		       (g.subject_type = 'group' AND g.subject_id IN (SELECT group_id FROM identity.group_members WHERE user_id = $1)))
		ORDER BY g.resource_id, CASE g.permission WHEN 'manage' THEN 3 WHEN 'edit' THEN 2 ELSE 1 END DESC`,
		userID, resourceType)
	if err != nil {
		return nil, fmt.Errorf("list shared: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (authz.Shared, error) {
		var sh authz.Shared
		return sh, row.Scan(&sh.ResourceID, &sh.Permission, &sh.GrantedBy)
	})
}

// DisplayNames returns the display names of users by ID.
func (s *Service) DisplayNames(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id, display_name FROM identity.users WHERE id = ANY($1)`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("load display names: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]string, len(userIDs))
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("read display names: %w", err)
		}
		out[id] = name
	}
	return out, rows.Err()
}

// DirectoryEntry is a user or group that something can be shared with.
type DirectoryEntry struct {
	Type    authz.SubjectType `json:"type"`
	ID      uuid.UUID         `json:"id"`
	Name    string            `json:"name"`
	Detail  string            `json:"detail"`  // username, or group description
	Picture string            `json:"picture"` // users only
}

// Directory finds active users and groups by name, for sharing. Every
// signed-in user can see this list: sharing needs it (see ADR 0006).
func (s *Service) Directory(ctx context.Context, query string) ([]DirectoryEntry, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(query)) + "%"
	rows, err := s.db.Query(ctx, `
		(SELECT 'user', id, display_name, username, avatar_key FROM identity.users
		 WHERE disabled_at IS NULL AND (display_name ILIKE $1 OR username ILIKE $1)
		 ORDER BY display_name LIMIT 20)
		UNION ALL
		(SELECT 'group', id, name, description, NULL FROM identity.groups
		 WHERE name ILIKE $1 OR description ILIKE $1 ORDER BY name LIMIT 20)`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search directory: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DirectoryEntry, error) {
		var e DirectoryEntry
		var avatar *string
		err := row.Scan(&e.Type, &e.ID, &e.Name, &e.Detail, &avatar)
		e.Picture = s.avatarURL(avatar)
		return e, err
	})
}

func (s *Service) handleDirectory(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Directory(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Entries []DirectoryEntry `json:"entries"`
	}{entries})
}
