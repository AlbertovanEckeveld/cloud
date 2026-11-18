package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Errors specific to account administration.
var (
	ErrLastAdmin    = errors.New("at least one active administrator must remain")
	ErrSelf         = errors.New("action not allowed on your own account")
	ErrUserDisabled = errors.New("account is disabled")
)

// AccountStatus summarises whether an account can be used.
type AccountStatus string

const (
	StatusPending  AccountStatus = "pending" // waiting for activation
	StatusActive   AccountStatus = "active"
	StatusDisabled AccountStatus = "disabled"
)

// AdminUser is an account as shown to administrators.
type AdminUser struct {
	User
	Status        AccountStatus `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
	LastLoginAt   *time.Time    `json:"last_login_at"`
	HasTOTP       bool          `json:"has_totp"`
	Passkeys      int           `json:"passkeys"`
	RecoveryCodes int           `json:"recovery_codes"`
	Groups        []GroupRef    `json:"groups"`
	// Roles the user has, directly or through a group.
	Roles []RoleRef `json:"roles"`
}

// GroupRef names a group.
type GroupRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// UserUpdate holds the editable fields of an account; nil means unchanged.
type UserUpdate struct {
	DisplayName *string
	// Email: nil leaves it unchanged, "" removes it.
	Email *string
}

const adminUserQuery = `
	SELECT u.id, u.username, u.display_name, COALESCE(u.email, ''), u.created_at, u.last_login_at,
	       CASE WHEN u.disabled_at IS NOT NULL THEN 'disabled'
	            WHEN t.confirmed_at IS NULL THEN 'pending'
	            ELSE 'active' END,
	       t.confirmed_at IS NOT NULL,
	       (SELECT count(*) FROM identity.passkeys p WHERE p.user_id = u.id),
	       (SELECT count(*) FROM identity.recovery_codes r WHERE r.user_id = u.id AND r.used_at IS NULL),
	       COALESCE((SELECT json_agg(json_build_object('id', g.id, 'name', g.name) ORDER BY g.name)
	                 FROM identity.group_members m JOIN identity.groups g ON g.id = m.group_id
	                 WHERE m.user_id = u.id), '[]'),
	       COALESCE((SELECT json_agg(json_build_object('id', r.id, 'name', r.name, 'group', g.name) ORDER BY r.name, g.name NULLS FIRST)
	                 FROM identity.role_assignments a JOIN identity.roles r ON r.id = a.role_id
	                 LEFT JOIN identity.groups g ON g.id = a.group_id
	                 WHERE a.user_id = u.id
	                    OR a.group_id IN (SELECT group_id FROM identity.group_members WHERE user_id = u.id)), '[]')
	FROM identity.users u
	LEFT JOIN identity.totp_credentials t ON t.user_id = u.id`

func scanAdminUser(row pgx.CollectableRow) (AdminUser, error) {
	var u AdminUser
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.CreatedAt, &u.LastLoginAt,
		&u.Status, &u.HasTOTP, &u.Passkeys, &u.RecoveryCodes, &u.Groups, &u.Roles)
	u.CreatedAt = u.CreatedAt.UTC()
	if u.LastLoginAt != nil {
		*u.LastLoginAt = u.LastLoginAt.UTC()
	}
	return u, err
}

// ListUsers returns all accounts, sorted by username.
func (s *Service) ListUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.Query(ctx, adminUserQuery+` ORDER BY u.username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users, err := pgx.CollectRows(rows, scanAdminUser)
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	return users, nil
}

// GetUser returns one account.
func (s *Service) GetUser(ctx context.Context, id uuid.UUID) (AdminUser, error) {
	rows, err := s.db.Query(ctx, adminUserQuery+` WHERE u.id = $1`, id)
	if err != nil {
		return AdminUser{}, fmt.Errorf("get user: %w", err)
	}
	u, err := pgx.CollectExactlyOneRow(rows, scanAdminUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("read user: %w", err)
	}
	return u, nil
}

// InviteUser creates an account and returns its activation link. Only for
// administrators who signed in recently.
func (s *Service) InviteUser(ctx context.Context, c Client, actor Session, in NewUser) (User, Activation, error) {
	if err := s.requireRecentAuth(actor); err != nil {
		return User{}, Activation{}, err
	}
	u, err := s.createUser(ctx, c, &actor.UserID, in)
	if err != nil {
		return User{}, Activation{}, err
	}
	a, err := s.issueActivation(ctx, c, &actor.UserID, u.Username)
	return u, a, err
}

// ResetUser revokes an account's credentials and returns a new activation link.
// It is refused for accounts with permissions the actor lacks: the new link
// would otherwise be a way into a stronger account.
func (s *Service) ResetUser(ctx context.Context, c Client, actor Principal, id uuid.UUID) (Activation, error) {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return Activation{}, err
	}
	if id == actor.Session.UserID {
		return Activation{}, ErrSelf
	}
	if err := s.requireOutranks(ctx, s.db, actor, id); err != nil {
		return Activation{}, err
	}
	var username string
	if err := s.db.QueryRow(ctx, `SELECT username FROM identity.users WHERE id = $1`, id).Scan(&username); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Activation{}, ErrNotFound
		}
		return Activation{}, fmt.Errorf("load user: %w", err)
	}
	return s.issueActivation(ctx, c, &actor.Session.UserID, username)
}

// UpdateUser changes the display name or e-mail address of an account,
// but never of an account with permissions the actor lacks (a new address
// would let them receive its reset links). Roles are changed with SetRole.
func (s *Service) UpdateUser(ctx context.Context, c Client, actor Principal, id uuid.UUID, upd UserUpdate) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	if upd.DisplayName != nil {
		name := strings.TrimSpace(*upd.DisplayName)
		if err := validateDisplayName(name); err != nil {
			return err
		}
		upd.DisplayName = &name
	}
	var email *string
	if upd.Email != nil {
		var err error
		if email, err = normalizeEmail(*upd.Email); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.requireOutranks(ctx, tx, actor, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE identity.users
			SET display_name = COALESCE($2, display_name),
			    email = CASE WHEN $4 THEN $5 ELSE email END, updated_at = $3
			WHERE id = $1`, id, upd.DisplayName, s.now(), upd.Email != nil, email)
		if conflict := userConflict(err); conflict != nil {
			return conflict
		}
		if err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		details := map[string]any{}
		if upd.DisplayName != nil {
			details["display_name"] = *upd.DisplayName
		}
		if upd.Email != nil {
			details["email_changed"] = true // the address itself stays out of the log
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventUserUpdated, actor: &actor.Session.UserID, subject: &id, details: details})
	})
}

// SetUserDisabled disables or re-enables an account. Disabling signs the
// user out everywhere and voids open activation links and ceremonies.
// Only accounts without permissions the actor lacks; never the last
// administrator.
func (s *Service) SetUserDisabled(ctx context.Context, c Client, actor Principal, id uuid.UUID, disabled bool) error {
	if err := s.requireRecentAuth(actor.Session); err != nil {
		return err
	}
	if id == actor.Session.UserID {
		return ErrSelf
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		if err := lockRoles(ctx, tx); err != nil {
			return err
		}
		if err := s.requireOutranks(ctx, tx, actor, id); err != nil {
			return err
		}
		var disabledAt *time.Time
		if disabled {
			disabledAt = &now
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.users SET disabled_at = $2, updated_at = $3 WHERE id = $1`, id, disabledAt, now)
		if err != nil {
			return fmt.Errorf("set disabled: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		event := EventUserEnabled
		if disabled {
			event = EventUserDisabled
			revocations := []struct {
				sql  string
				args []any
			}{
				{`DELETE FROM identity.sessions WHERE user_id = $1`, []any{id}},
				{`DELETE FROM identity.webauthn_challenges WHERE user_id = $1`, []any{id}},
				{`UPDATE identity.activation_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, []any{id, now}},
			}
			for _, r := range revocations {
				if _, err := tx.Exec(ctx, r.sql, r.args...); err != nil {
					return fmt.Errorf("revoke access: %w", err)
				}
			}
			if err := s.revokeAppAccess(ctx, tx, id, nil, ""); err != nil {
				return err
			}
		}
		if err := ensureAdminRemains(ctx, tx); err != nil {
			return err
		}
		return s.audit(ctx, tx, c, auditEntry{event: event, actor: &actor.Session.UserID, subject: &id})
	})
}
