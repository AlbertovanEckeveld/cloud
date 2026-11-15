package identity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// usernamePattern matches the CHECK constraint on identity.users.username.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// NewUser is the input for CreateUser.
type NewUser struct {
	Username    string
	DisplayName string
	IsAdmin     bool
}

// InputError is a validation error whose message is safe to show to users.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

func normalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validateDisplayName(s string) error {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 100 || !utf8.ValidString(s) {
		return &InputError{"De weergavenaam moet 1 tot 100 tekens lang zijn."}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return &InputError{"De weergavenaam bevat ongeldige tekens."}
		}
	}
	return nil
}

// CreateUser creates an account without credentials. Call IssueActivation
// to let the user set a password and second factor.
func (s *Service) CreateUser(ctx context.Context, c Client, in NewUser) (User, error) {
	u := User{
		ID:          newID(),
		Username:    normalizeUsername(in.Username),
		DisplayName: strings.TrimSpace(in.DisplayName),
		IsAdmin:     in.IsAdmin,
	}
	if !usernamePattern.MatchString(u.Username) {
		return User{}, &InputError{"De gebruikersnaam moet 2 tot 32 tekens zijn: kleine letters, cijfers, punt, streepje of underscore."}
	}
	if err := validateDisplayName(u.DisplayName); err != nil {
		return User{}, err
	}

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		_, err := tx.Exec(ctx, `
			INSERT INTO identity.users (id, username, display_name, is_admin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5)`,
			u.ID, u.Username, u.DisplayName, u.IsAdmin, now)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" {
			return ErrUserExists
		}
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{
			event: EventUserCreated, subject: &u.ID,
			details: map[string]any{"username": u.Username, "is_admin": u.IsAdmin},
		})
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// IssueActivation returns a one-time link with which the user sets a new
// password and enrols a new second factor. Issuing a link resets the
// account: the old password, second factor, sessions and links stop working.
// That makes it the recovery path for a user who lost their authenticator.
func (s *Service) IssueActivation(ctx context.Context, c Client, username string) (string, error) {
	token, hash := newToken()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		var u User
		err := tx.QueryRow(ctx, `SELECT id, username FROM identity.users WHERE username = $1 FOR UPDATE`,
			normalizeUsername(username)).Scan(&u.ID, &u.Username)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return fmt.Errorf("load user: %w", err)
		}
		resets := []struct {
			sql  string
			args []any
		}{
			{`UPDATE identity.users SET password_hash = NULL, updated_at = $2 WHERE id = $1`, []any{u.ID, now}},
			{`DELETE FROM identity.totp_credentials WHERE user_id = $1`, []any{u.ID}},
			{`DELETE FROM identity.sessions WHERE user_id = $1`, []any{u.ID}},
			{`UPDATE identity.activation_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, []any{u.ID, now}},
		}
		for _, r := range resets {
			if _, err := tx.Exec(ctx, r.sql, r.args...); err != nil {
				return fmt.Errorf("reset credentials: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.activation_tokens (token_hash, user_id, created_at, expires_at)
			VALUES ($1, $2, $3, $4)`, hash, u.ID, now, now.Add(activationTTL)); err != nil {
			return fmt.Errorf("insert activation token: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventActivationIssued, subject: &u.ID})
	})
	if err != nil {
		return "", err
	}
	// The token is in the fragment (#): browsers never send it to the server
	// in requests or Referer headers, so it cannot end up in access logs.
	return s.opts.PublicURL.String() + "/activate#" + token, nil
}
