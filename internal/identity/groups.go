package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrGroupExists means the group name is already taken.
var ErrGroupExists = errors.New("group name already taken")

// groupNamePattern matches the CHECK constraint on identity.groups.name.
var groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// Group is a named set of users.
type Group struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// GroupDetail is a group with its members.
type GroupDetail struct {
	Group
	Members []User `json:"members"`
	// Roles given to the group: every member has them.
	Roles []RoleRef `json:"roles"`
}

// GroupInput holds the editable fields of a group.
type GroupInput struct {
	Name        string
	Description string
}

func (in *GroupInput) normalize() error {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.Description = strings.TrimSpace(in.Description)
	if !groupNamePattern.MatchString(in.Name) {
		return &InputError{"De groepsnaam moet 2 tot 32 tekens zijn: kleine letters, cijfers, punt, streepje of underscore."}
	}
	if utf8.RuneCountInString(in.Description) > 200 {
		return &InputError{"De omschrijving mag maximaal 200 tekens zijn."}
	}
	return nil
}

func uniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

// ListGroups returns all groups, sorted by name.
func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.name, g.description, g.created_at,
		       (SELECT count(*) FROM identity.group_members m WHERE m.group_id = g.id)
		FROM identity.groups g ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Group, error) {
		var g Group
		err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.MemberCount)
		g.CreatedAt = g.CreatedAt.UTC()
		return g, err
	})
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	return groups, nil
}

// GetGroup returns a group with its members.
func (s *Service) GetGroup(ctx context.Context, id uuid.UUID) (GroupDetail, error) {
	var g GroupDetail
	err := s.db.QueryRow(ctx, `SELECT id, name, description, created_at FROM identity.groups WHERE id = $1`, id).
		Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupDetail{}, ErrNotFound
	}
	if err != nil {
		return GroupDetail{}, fmt.Errorf("get group: %w", err)
	}
	g.CreatedAt = g.CreatedAt.UTC()
	rows, err := s.db.Query(ctx, `
		SELECT u.id, u.username, u.display_name, COALESCE(u.email, '')
		FROM identity.group_members m JOIN identity.users u ON u.id = m.user_id
		WHERE m.group_id = $1 ORDER BY u.username`, id)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("list members: %w", err)
	}
	g.Members, err = pgx.CollectRows(rows, pgx.RowToStructByPos[User])
	if err != nil {
		return GroupDetail{}, fmt.Errorf("read members: %w", err)
	}
	g.MemberCount = len(g.Members)
	rows, err = s.db.Query(ctx, `
		SELECT r.id, r.name FROM identity.role_assignments a JOIN identity.roles r ON r.id = a.role_id
		WHERE a.group_id = $1 ORDER BY r.name`, id)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("list group roles: %w", err)
	}
	g.Roles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RoleRef, error) {
		var r RoleRef
		return r, row.Scan(&r.ID, &r.Name)
	})
	if err != nil {
		return GroupDetail{}, fmt.Errorf("read group roles: %w", err)
	}
	return g, nil
}

// UserGroups returns the groups a user belongs to.
func (s *Service) UserGroups(ctx context.Context, userID uuid.UUID) ([]GroupRef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT g.id, g.name FROM identity.group_members m JOIN identity.groups g ON g.id = m.group_id
		WHERE m.user_id = $1 ORDER BY g.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("user groups: %w", err)
	}
	groups, err := pgx.CollectRows(rows, pgx.RowToStructByPos[GroupRef])
	if err != nil {
		return nil, fmt.Errorf("read user groups: %w", err)
	}
	return groups, nil
}

// CreateGroup creates an empty group.
func (s *Service) CreateGroup(ctx context.Context, c Client, actor Session, in GroupInput) (Group, error) {
	if err := s.requireRecentAuth(actor); err != nil {
		return Group{}, err
	}
	if err := in.normalize(); err != nil {
		return Group{}, err
	}
	g := Group{ID: newID(), Name: in.Name, Description: in.Description, CreatedAt: s.now().UTC()}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identity.groups (id, name, description, created_at) VALUES ($1, $2, $3, $4)`,
			g.ID, g.Name, g.Description, g.CreatedAt)
		if uniqueViolation(err) {
			return ErrGroupExists
		}
		if err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventGroupCreated, actor: &actor.UserID, details: map[string]any{"group": g.Name}})
	})
	if err != nil {
		return Group{}, err
	}
	return g, nil
}

// UpdateGroup renames a group or changes its description.
func (s *Service) UpdateGroup(ctx context.Context, c Client, actor Session, id uuid.UUID, in GroupInput) error {
	if err := s.requireRecentAuth(actor); err != nil {
		return err
	}
	if err := in.normalize(); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE identity.groups SET name = $2, description = $3 WHERE id = $1`, id, in.Name, in.Description)
		if uniqueViolation(err) {
			return ErrGroupExists
		}
		if err != nil {
			return fmt.Errorf("update group: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventGroupUpdated, actor: &actor.UserID, details: map[string]any{"group": in.Name}})
	})
}

// DeleteGroup removes a group; its members stay as they are.
// A group with roles can only be deleted by someone who could take those
// roles away (ADR 0022), and never when it holds the last administrator.
func (s *Service) DeleteGroup(ctx context.Context, c Client, actor Principal, id uuid.UUID) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		grants, err := groupPermissions(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requireMayGrant(actor, grants); err != nil {
			return err
		}
		var name string
		err = tx.QueryRow(ctx, `DELETE FROM identity.groups WHERE id = $1 RETURNING name`, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" {
			return ErrGroupInUse // foreign key: an app is restricted to this group
		}
		if err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		// Shares with the group end with it.
		if _, err := tx.Exec(ctx, `DELETE FROM identity.grants WHERE subject_type = 'group' AND subject_id = $1`, id); err != nil {
			return fmt.Errorf("delete group shares: %w", err)
		}
		if err := ensureAdminRemains(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventGroupDeleted, actor: &actor.Session.UserID, details: map[string]any{"group": name}})
	})
}

// SetGroupMember adds a user to a group or removes them. Both are idempotent.
// For a group with roles this gives or takes those roles, so it follows the
// rules for assigning roles (ADR 0022).
func (s *Service) SetGroupMember(ctx context.Context, c Client, actor Principal, groupID, userID uuid.UUID, member bool) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		grants, err := groupPermissions(ctx, tx, groupID)
		if err != nil {
			return err
		}
		if err := requireMayGrant(actor, grants); err != nil {
			return err
		}
		var group string
		if err := tx.QueryRow(ctx, `SELECT name FROM identity.groups WHERE id = $1`, groupID).Scan(&group); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("load group: %w", err)
		}
		var tag pgconn.CommandTag
		event := EventGroupMemberRemoved
		if member {
			event = EventGroupMemberAdded
			tag, err = tx.Exec(ctx, `
				INSERT INTO identity.group_members (group_id, user_id, added_at)
				SELECT $1, id, $3 FROM identity.users WHERE id = $2
				ON CONFLICT DO NOTHING`, groupID, userID, s.now())
		} else {
			tag, err = tx.Exec(ctx, `DELETE FROM identity.group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
		}
		if err != nil {
			return fmt.Errorf("update membership: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Already in the requested state, or the user does not exist.
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE id = $1)`, userID).Scan(&exists); err != nil {
				return fmt.Errorf("check user: %w", err)
			}
			if !exists {
				return ErrNotFound
			}
			return nil
		}
		if !member {
			if err := ensureAdminRemains(ctx, tx); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, c, auditEntry{event: event, actor: &actor.Session.UserID, subject: &userID, details: map[string]any{"group": group}})
	})
}
