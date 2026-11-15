package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// challengeTTL bounds how long a WebAuthn ceremony may take.
const challengeTTL = 5 * time.Minute

// Passkey is a registered WebAuthn credential as shown to its owner.
type Passkey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// Synced is true for passkeys that a password manager or platform
	// (iCloud Keychain, Google Password Manager) copies between devices.
	Synced bool `json:"synced"`
}

// Ceremony is what the browser needs to start a WebAuthn ceremony. The
// options go to navigator.credentials.create() or .get(); the ID must come
// back with the result.
type Ceremony struct {
	ChallengeID uuid.UUID `json:"challenge_id"`
	Options     any       `json:"options"`
}

// webauthnUser adapts an account to the library's User interface. The user
// handle is the account's UUID: opaque and free of personal data, as
// WebAuthn §14.6.1 recommends.
type webauthnUser struct {
	id          uuid.UUID
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.id[:] }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) loadWebauthnUser(ctx context.Context, q dbtx, userID uuid.UUID) (*webauthnUser, error) {
	u := &webauthnUser{id: userID}
	if err := q.QueryRow(ctx, `SELECT username, display_name FROM identity.users WHERE id = $1`, userID).
		Scan(&u.name, &u.displayName); err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	rows, err := q.Query(ctx, `SELECT credential FROM identity.passkeys WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("load passkeys: %w", err)
	}
	u.credentials, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (webauthn.Credential, error) {
		var raw []byte
		var c webauthn.Credential
		if err := row.Scan(&raw); err != nil {
			return c, err
		}
		return c, json.Unmarshal(raw, &c)
	})
	if err != nil {
		return nil, fmt.Errorf("read passkeys: %w", err)
	}
	return u, nil
}

// storeChallenge keeps the ceremony state server-side until the browser answers.
func (s *Service) storeChallenge(ctx context.Context, purpose string, userID, sessionID *uuid.UUID, data *webauthn.SessionData) (uuid.UUID, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode challenge: %w", err)
	}
	id := newID()
	if _, err := s.db.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (id, purpose, user_id, session_id, session_data, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, purpose, userID, sessionID, raw, s.now().Add(challengeTTL)); err != nil {
		return uuid.Nil, fmt.Errorf("store challenge: %w", err)
	}
	return id, nil
}

// takeChallenge removes and returns a pending ceremony. Deleting it first
// makes every challenge single-use, even under concurrent requests.
func (s *Service) takeChallenge(ctx context.Context, id uuid.UUID, purpose string, sessionID *uuid.UUID) (webauthn.SessionData, error) {
	var raw []byte
	var expires time.Time
	err := s.db.QueryRow(ctx, `
		DELETE FROM identity.webauthn_challenges
		WHERE id = $1 AND purpose = $2 AND session_id IS NOT DISTINCT FROM $3
		RETURNING session_data, expires_at`, id, purpose, sessionID).Scan(&raw, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return webauthn.SessionData{}, ErrChallengeInvalid
	}
	if err != nil {
		return webauthn.SessionData{}, fmt.Errorf("load challenge: %w", err)
	}
	if !s.now().Before(expires) {
		return webauthn.SessionData{}, ErrChallengeInvalid
	}
	var data webauthn.SessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return webauthn.SessionData{}, fmt.Errorf("decode challenge: %w", err)
	}
	return data, nil
}

// BeginPasskeyRegistration starts adding a passkey to the signed-in user.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, sess Session) (Ceremony, error) {
	if err := s.requireRecentAuth(sess); err != nil {
		return Ceremony{}, err
	}
	user, err := s.loadWebauthnUser(ctx, s.db, sess.UserID)
	if err != nil {
		return Ceremony{}, err
	}
	// Exclude existing credentials so the same authenticator is not registered twice.
	exclude := make([]protocol.CredentialDescriptor, len(user.credentials))
	for i, c := range user.credentials {
		exclude[i] = c.Descriptor()
	}
	options, data, err := s.webauthn.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired, // discoverable: no username needed
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired, // biometrics or PIN on every use
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(exclude),
	)
	if err != nil {
		return Ceremony{}, fmt.Errorf("begin registration: %w", err)
	}
	id, err := s.storeChallenge(ctx, "register", &sess.UserID, &sess.ID, data)
	if err != nil {
		return Ceremony{}, err
	}
	return Ceremony{ChallengeID: id, Options: options}, nil
}

// FinishPasskeyRegistration verifies the browser's answer and stores the passkey.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, c Client, sess Session, challengeID uuid.UUID, name string, credential json.RawMessage) (Passkey, error) {
	if err := s.requireRecentAuth(sess); err != nil {
		return Passkey{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Passkey"
	}
	if utf8.RuneCountInString(name) > 64 {
		return Passkey{}, &InputError{"De naam mag maximaal 64 tekens zijn."}
	}
	data, err := s.takeChallenge(ctx, challengeID, "register", &sess.ID)
	if err != nil {
		return Passkey{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return Passkey{}, fmt.Errorf("%w: %v", ErrPasskeyRejected, err)
	}
	user, err := s.loadWebauthnUser(ctx, s.db, sess.UserID)
	if err != nil {
		return Passkey{}, err
	}
	cred, err := s.webauthn.CreateCredential(user, data, parsed)
	if err != nil {
		return Passkey{}, fmt.Errorf("%w: %v", ErrPasskeyRejected, err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return Passkey{}, fmt.Errorf("encode credential: %w", err)
	}

	pk := Passkey{ID: newID(), Name: name, CreatedAt: s.now().UTC(), Synced: cred.Flags.BackupEligible}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.passkeys (id, user_id, credential_id, name, credential, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, pk.ID, sess.UserID, cred.ID, pk.Name, raw, pk.CreatedAt); err != nil {
			return fmt.Errorf("store passkey: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventPasskeyAdded, actor: &sess.UserID, details: map[string]any{"name": pk.Name}})
	})
	if err != nil {
		return Passkey{}, err
	}
	return pk, nil
}

// ListPasskeys returns the user's passkeys, newest first.
func (s *Service) ListPasskeys(ctx context.Context, userID uuid.UUID) ([]Passkey, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, created_at, last_used_at, (credential->'flags'->>'backupEligible')::boolean
		FROM identity.passkeys WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	passkeys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Passkey, error) {
		var p Passkey
		var synced *bool
		err := row.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.LastUsedAt, &synced)
		p.CreatedAt = p.CreatedAt.UTC()
		if p.LastUsedAt != nil {
			*p.LastUsedAt = p.LastUsedAt.UTC()
		}
		p.Synced = synced != nil && *synced
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read passkeys: %w", err)
	}
	return passkeys, nil
}

// DeletePasskey removes one of the user's passkeys.
func (s *Service) DeletePasskey(ctx context.Context, c Client, sess Session, id uuid.UUID) error {
	if err := s.requireRecentAuth(sess); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM identity.passkeys WHERE id = $1 AND user_id = $2 RETURNING name`,
			id, sess.UserID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventPasskeyRemoved, actor: &sess.UserID, details: map[string]any{"name": name}})
	})
}

// BeginPasskeyLogin starts a login without username: the browser offers the
// passkeys it holds for this site.
func (s *Service) BeginPasskeyLogin(ctx context.Context, c Client) (Ceremony, error) {
	if err := s.checkThrottle(ctx, throttleIP.key(c.IP)); err != nil {
		return Ceremony{}, err
	}
	options, data, err := s.webauthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return Ceremony{}, fmt.Errorf("begin login: %w", err)
	}
	id, err := s.storeChallenge(ctx, "login", nil, nil, data)
	if err != nil {
		return Ceremony{}, err
	}
	return Ceremony{ChallengeID: id, Options: options}, nil
}

// FinishPasskeyLogin verifies the assertion and returns a full session token.
// A passkey with user verification proves possession and a PIN or biometric,
// so it counts as two factors on its own.
func (s *Service) FinishPasskeyLogin(ctx context.Context, c Client, challengeID uuid.UUID, credential json.RawMessage) (string, error) {
	ipKey := throttleIP.key(c.IP)
	if err := s.checkThrottle(ctx, ipKey); err != nil {
		return "", err
	}
	data, err := s.takeChallenge(ctx, challengeID, "login", nil)
	if err != nil {
		return "", err
	}
	fail := func(reason string, subject *uuid.UUID, cause error) (string, error) {
		s.recordFailures(ctx, map[string]throttleRule{ipKey: throttleIP})
		s.auditBestEffort(ctx, c, auditEntry{event: EventPasskeyFailed, subject: subject, details: map[string]any{"reason": reason}})
		return "", fmt.Errorf("%w: %v", ErrPasskeyRejected, cause)
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return fail("malformed", nil, err)
	}
	var owner *uuid.UUID
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		id, err := uuid.FromBytes(userHandle)
		if err != nil {
			return nil, err
		}
		owner = &id
		var disabled bool
		if err := s.db.QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM identity.users WHERE id = $1`, id).Scan(&disabled); err != nil {
			return nil, err
		}
		if disabled {
			return nil, errors.New("account disabled")
		}
		return s.loadWebauthnUser(ctx, s.db, id)
	}
	_, cred, err := s.webauthn.ValidatePasskeyLogin(handler, data, parsed)
	if err != nil {
		return fail("invalid_assertion", owner, err)
	}
	if cred.Authenticator.CloneWarning {
		// The signature counter went backwards: two copies of this
		// authenticator may exist (WebAuthn §6.1.1). Refuse and record it.
		return fail("clone_warning", owner, errors.New("signature counter did not increase"))
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("encode credential: %w", err)
	}

	var token string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE identity.passkeys SET credential = $3, last_used_at = $4
			WHERE credential_id = $1 AND user_id = $2`, cred.ID, *owner, raw, s.now())
		if err != nil {
			return fmt.Errorf("update passkey: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrPasskeyRejected
		}
		if token, err = s.createSession(ctx, tx, *owner, StateAuthenticated, nil, c, sessionMaxTTL); err != nil {
			return err
		}
		return s.recordLogin(ctx, tx, c, *owner, "passkey")
	})
	return token, err
}
