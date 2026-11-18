package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrEscalation means the actor tried to give, take or use more than they
// have themselves (ADR 0022).
var ErrEscalation = errors.New("identity: permission escalation")

// Role is a named bundle of permissions.
type Role struct {
	ID          uuid.UUID    `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"` // all of them for the built-in role
	Builtin     bool         `json:"builtin"`
	Users       []GroupRef   `json:"users"`  // given directly (ID and display name)
	Groups      []GroupRef   `json:"groups"` // given to these groups
}

// RoleRef names a role a user has, and the group it comes through (nil:
// given directly).
type RoleRef struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Group *string   `json:"group"`
}

// RoleInput is what an administrator sets on a role.
type RoleInput struct {
	Name        string
	Description string
	Permissions []Permission
}

// roleLockID serialises every change that can affect who holds a role, so
// two concurrent changes cannot both remove "the other" last administrator.
const roleLockID = 0x726f6c6573 // "roles"

func lockRoles(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, roleLockID); err != nil {
		return fmt.Errorf("lock roles: %w", err)
	}
	return nil
}

// ensureAdminRemains fails with ErrLastAdmin when no enabled user holds the
// built-in role any more, directly or through a group. Call it inside the
// transaction that made the change, after lockRoles.
func ensureAdminRemains(ctx context.Context, tx pgx.Tx) error {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity.users u
			WHERE u.disabled_at IS NULL AND u.id IN (
				SELECT user_id FROM identity.role_assignments WHERE role_id = $1 AND user_id IS NOT NULL
				UNION
				SELECT m.user_id FROM identity.role_assignments a
				JOIN identity.group_members m ON m.group_id = a.group_id WHERE a.role_id = $1))`, AdminRoleID).Scan(&ok)
	if err != nil {
		return fmt.Errorf("check administrators: %w", err)
	}
	if !ok {
		return ErrLastAdmin
	}
	return nil
}

// rolePermissions returns what a role grants; all permissions for the
// built-in role.
func rolePermissions(builtin bool, stored []string) []Permission {
	if builtin {
		return allPermissions()
	}
	out := []Permission{}
	for _, p := range stored {
		if KnownPermission(Permission(p)) {
			out = append(out, Permission(p))
		}
	}
	return out
}

// groupPermissions returns what membership of a group grants.
func groupPermissions(ctx context.Context, q dbtx, groupID uuid.UUID) ([]Permission, error) {
	rows, err := q.Query(ctx, `
		SELECT r.builtin, r.permissions FROM identity.role_assignments a
		JOIN identity.roles r ON r.id = a.role_id WHERE a.group_id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("load group roles: %w", err)
	}
	set := PermissionSet{}
	var builtin bool
	var perms []string
	_, err = pgx.ForEachRow(rows, []any{&builtin, &perms}, func() error {
		for _, p := range rolePermissions(builtin, perms) {
			set[p] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load group roles: %w", err)
	}
	return set.List(), nil
}

// requireOutranks allows an action on another account only when that
// account has no permission the actor lacks: otherwise resetting or
// re-addressing it would be a way into a stronger account.
func (s *Service) requireOutranks(ctx context.Context, q dbtx, actor Principal, target uuid.UUID) error {
	theirs, err := s.permissionsOf(ctx, q, target)
	if err != nil {
		return err
	}
	if !actor.Permissions.Covers(theirs.List()) {
		return ErrEscalation
	}
	return nil
}

// requireMayGrant allows giving or taking these permissions only to an
// actor who may assign roles and holds every one of them.
func requireMayGrant(actor Principal, perms []Permission) error {
	if len(perms) == 0 {
		return nil
	}
	if !actor.Can(PermRolesAssign) || !actor.Permissions.Covers(perms) {
		return ErrEscalation
	}
	return nil
}

const roleColumns = `
	r.id, r.name, r.description, r.permissions, r.builtin,
	COALESCE((SELECT json_agg(json_build_object('id', u.id, 'name', u.display_name) ORDER BY u.display_name)
	          FROM identity.role_assignments a JOIN identity.users u ON u.id = a.user_id
	          WHERE a.role_id = r.id), '[]'),
	COALESCE((SELECT json_agg(json_build_object('id', g.id, 'name', g.name) ORDER BY g.name)
	          FROM identity.role_assignments a JOIN identity.groups g ON g.id = a.group_id
	          WHERE a.role_id = r.id), '[]')`

func scanRole(row pgx.CollectableRow) (Role, error) {
	var r Role
	var perms []string
	err := row.Scan(&r.ID, &r.Name, &r.Description, &perms, &r.Builtin, &r.Users, &r.Groups)
	r.Permissions = rolePermissions(r.Builtin, perms)
	return r, err
}

// ListRoles returns every role, the built-in one first.
func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.Query(ctx, `SELECT `+roleColumns+` FROM identity.roles r ORDER BY r.builtin DESC, r.name`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	roles, err := pgx.CollectRows(rows, scanRole)
	if err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	return roles, nil
}

func (s *Service) getRole(ctx context.Context, q dbtx, id uuid.UUID) (Role, error) {
	rows, err := q.Query(ctx, `SELECT `+roleColumns+` FROM identity.roles r WHERE r.id = $1`, id)
	if err != nil {
		return Role{}, fmt.Errorf("get role: %w", err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("read role: %w", err)
	}
	return r, nil
}

func validateRole(in RoleInput) (RoleInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if n := utf8.RuneCountInString(in.Name); n == 0 || n > 60 || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return in, &InputError{"Geef de rol een naam van 1 tot 60 tekens."}
	}
	if utf8.RuneCountInString(in.Description) > 300 || strings.IndexFunc(in.Description, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) >= 0 {
		return in, &InputError{"De omschrijving mag hoogstens 300 tekens zijn."}
	}
	seen := map[Permission]bool{}
	clean := []Permission{}
	for _, p := range in.Permissions {
		if !KnownPermission(p) {
			return in, &InputError{"Onbekend recht: " + string(p)}
		}
		if !seen[p] {
			seen[p] = true
			clean = append(clean, p)
		}
	}
	in.Permissions = clean
	return in, nil
}

func roleConflict(err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
		return &InputError{"Er bestaat al een rol met deze naam."}
	}
	return nil
}

// CreateRole adds a role. Nobody can put a permission in a role that they
// do not have themselves.
func (s *Service) CreateRole(ctx context.Context, c Client, actor Principal, in RoleInput) (Role, error) {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return Role{}, err
	}
	in, err := validateRole(in)
	if err != nil {
		return Role{}, err
	}
	if !actor.Permissions.Covers(in.Permissions) {
		return Role{}, ErrEscalation
	}
	id := newID()
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		_, err := tx.Exec(ctx, `
			INSERT INTO identity.roles (id, name, description, permissions, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)`, id, in.Name, in.Description, in.Permissions, now)
		if conflict := roleConflict(err); conflict != nil {
			return conflict
		}
		if err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventRoleCreated, actor: &actor.Session.UserID,
			details: map[string]any{"role": in.Name, "permissions": in.Permissions}})
	})
	if err != nil {
		return Role{}, err
	}
	return s.getRole(ctx, s.db, id)
}

// UpdateRole changes a role. The built-in role cannot be changed. Changing
// what a role grants changes it for everyone who has it, so the actor must
// hold every permission the role had and every one it gets.
func (s *Service) UpdateRole(ctx context.Context, c Client, actor Principal, id uuid.UUID, in RoleInput) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	in, err := validateRole(in)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		old, err := s.getRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Builtin {
			return &InputError{"De rol Beheerder kan niet worden gewijzigd."}
		}
		if !actor.Permissions.Covers(old.Permissions) || !actor.Permissions.Covers(in.Permissions) {
			return ErrEscalation
		}
		_, err = tx.Exec(ctx, `
			UPDATE identity.roles SET name = $2, description = $3, permissions = $4, updated_at = $5 WHERE id = $1`,
			id, in.Name, in.Description, in.Permissions, s.now())
		if conflict := roleConflict(err); conflict != nil {
			return conflict
		}
		if err != nil {
			return fmt.Errorf("update role: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventRoleUpdated, actor: &actor.Session.UserID,
			details: map[string]any{"role": in.Name, "permissions": in.Permissions}})
	})
}

// DeleteRole removes a role and with it every assignment of it.
func (s *Service) DeleteRole(ctx context.Context, c Client, actor Principal, id uuid.UUID) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		old, err := s.getRole(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Builtin {
			return &InputError{"De rol Beheerder kan niet worden verwijderd."}
		}
		if !actor.Permissions.Covers(old.Permissions) {
			return ErrEscalation
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.roles WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete role: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventRoleDeleted, actor: &actor.Session.UserID,
			details: map[string]any{"role": old.Name}})
	})
}

// RoleSubject is who a role is given to: a user or a group.
type RoleSubject struct {
	User  *uuid.UUID
	Group *uuid.UUID
}

// SetRole gives a role to a user or group (on true) or takes it away. Both
// need identity.roles.assign and every permission of the role; taking the
// built-in role from the last administrator fails.
func (s *Service) SetRole(ctx context.Context, c Client, actor Principal, roleID uuid.UUID, to RoleSubject, on bool) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	if (to.User == nil) == (to.Group == nil) {
		return &InputError{"Kies een gebruiker of een groep."}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		role, err := s.getRole(ctx, tx, roleID)
		if err != nil {
			return err
		}
		if err := requireMayGrant(actor, role.Permissions); err != nil {
			return err
		}
		details := map[string]any{"role": role.Name}
		var subject *uuid.UUID
		if to.User != nil {
			subject = to.User
		} else {
			var name string
			if err := tx.QueryRow(ctx, `SELECT name FROM identity.groups WHERE id = $1`, *to.Group).Scan(&name); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return fmt.Errorf("load group: %w", err)
			}
			details["group"] = name
		}
		event := EventRoleAssigned
		if on {
			_, err = tx.Exec(ctx, `
				INSERT INTO identity.role_assignments (role_id, user_id, group_id, created_at) VALUES ($1, $2, $3, $4)
				ON CONFLICT DO NOTHING`, roleID, to.User, to.Group, s.now())
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" {
				return ErrNotFound // no such user
			}
		} else {
			event = EventRoleRevoked
			_, err = tx.Exec(ctx, `
				DELETE FROM identity.role_assignments
				WHERE role_id = $1 AND user_id IS NOT DISTINCT FROM $2 AND group_id IS NOT DISTINCT FROM $3`,
				roleID, to.User, to.Group)
		}
		if err != nil {
			return fmt.Errorf("set role: %w", err)
		}
		if !on {
			if err := ensureAdminRemains(ctx, tx); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, c, auditEntry{event: event, actor: &actor.Session.UserID, subject: subject, details: details})
	})
}

// UserRoles lists the roles a user has, directly and through groups.
func (s *Service) UserRoles(ctx context.Context, userID uuid.UUID) ([]RoleRef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.name, g.name FROM identity.role_assignments a
		JOIN identity.roles r ON r.id = a.role_id
		LEFT JOIN identity.groups g ON g.id = a.group_id
		WHERE a.user_id = $1 OR a.group_id IN (SELECT group_id FROM identity.group_members WHERE user_id = $1)
		ORDER BY r.name, g.name NULLS FIRST`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RoleRef, error) {
		var r RoleRef
		return r, row.Scan(&r.ID, &r.Name, &r.Group)
	})
	if err != nil {
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	return refs, nil
}
