package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Login checks a username and password. On success it creates a partial
// session that still needs the second factor (VerifyTOTP) and returns its token.
func (s *Service) Login(ctx context.Context, c Client, username, password string) (string, error) {
	username = normalizeUsername(username)
	userKey, ipKey := throttleUser.key(username), throttleIP.key(c.IP)
	failed := func(reason string, subject *uuid.UUID) {
		s.recordFailures(ctx, map[string]throttleRule{userKey: throttleUser, ipKey: throttleIP})
		details := map[string]any{"reason": reason}
		if subject == nil {
			// Unknown user: keep the attempted name for forensics, bounded in size.
			details["username"] = username[:min(len(username), 64)]
		}
		s.auditBestEffort(ctx, c, auditEntry{event: EventLoginFailed, subject: subject, details: details})
	}

	// Check the lock before hashing: a locked-out attacker costs us no CPU.
	if err := s.checkThrottle(ctx, userKey, ipKey); err != nil {
		return "", err
	}
	if len(password) > maxPasswordBytes {
		failed("bad_password", nil)
		return "", ErrInvalidCredentials
	}

	var (
		id          uuid.UUID
		hash        *string
		hasTOTP     bool
		disabled    bool
		lookupError error
	)
	lookupError = s.db.QueryRow(ctx, `
		SELECT u.id, u.password_hash, t.confirmed_at IS NOT NULL, u.disabled_at IS NOT NULL
		FROM identity.users u
		LEFT JOIN identity.totp_credentials t ON t.user_id = u.id
		WHERE u.username = $1`, username).Scan(&id, &hash, &hasTOTP, &disabled)
	if errors.Is(lookupError, pgx.ErrNoRows) || (lookupError == nil && hash == nil) {
		// Same work and same answer as a wrong password: no user enumeration.
		s.hasher.verifyDummy(ctx, password)
		if lookupError != nil {
			failed("unknown_user", nil)
		} else {
			failed("not_activated", &id)
		}
		return "", ErrInvalidCredentials
	}
	if lookupError != nil {
		return "", fmt.Errorf("load user: %w", lookupError)
	}

	match, rehash, err := s.hasher.verify(ctx, password, *hash)
	if err != nil {
		return "", fmt.Errorf("verify password: %w", err)
	}
	if !match {
		failed("bad_password", &id)
		return "", ErrInvalidCredentials
	}
	if disabled {
		// Checked after the password, so only someone who knows it could
		// tell a disabled account apart; they still get the generic answer.
		failed("disabled", &id)
		return "", ErrInvalidCredentials
	}
	if !hasTOTP {
		// Password set during activation, but enrolment never finished.
		// Only someone who knows the password learns this.
		failed("not_activated", &id)
		return "", ErrNotActivated
	}

	if err := s.clearThrottle(ctx, userKey); err != nil {
		return "", err
	}
	if rehash {
		s.upgradeHash(ctx, id, password)
	}
	token, _, err := s.createSession(ctx, s.db, id, StateMFARequired, nil, c, partialSessionTTL)
	return token, err
}

// upgradeHash re-hashes a password with the current parameters. Failure only
// means the upgrade happens on a later login.
func (s *Service) upgradeHash(ctx context.Context, userID uuid.UUID, password string) {
	newHash, err := s.hasher.hash(ctx, password)
	if err == nil {
		_, err = s.db.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, userID, newHash)
	}
	if err != nil {
		s.log.Warn("password hash upgrade failed", "user_id", userID, "error", err)
	}
}

// VerifyTOTP completes a login: it checks the code for a partial session and
// replaces it with a full session under a new token.
func (s *Service) VerifyTOTP(ctx context.Context, c Client, sess Session, code string) (string, error) {
	if sess.State != StateMFARequired {
		return "", ErrWrongState
	}
	key := throttleTOTP.key(sess.UserID.String())
	if err := s.checkThrottle(ctx, key); err != nil {
		return "", err
	}

	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// FOR UPDATE serialises concurrent attempts, so one code cannot be
		// used twice by racing requests.
		var sealed []byte
		var lastStep int64
		err := tx.QueryRow(ctx, `
			SELECT secret_sealed, last_used_step FROM identity.totp_credentials
			WHERE user_id = $1 AND confirmed_at IS NOT NULL FOR UPDATE`, sess.UserID).Scan(&sealed, &lastStep)
		if err != nil {
			return fmt.Errorf("load totp credential: %w", err)
		}
		secret, err := s.sealer.Open(sealed, totpAD(sess.UserID))
		if err != nil {
			return fmt.Errorf("open totp secret: %w", err)
		}
		step, ok := verifyTOTP(secret, code, s.now(), lastStep)
		if !ok {
			return ErrInvalidCode
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.totp_credentials SET last_used_step = $2 WHERE user_id = $1`, sess.UserID, step); err != nil {
			return fmt.Errorf("store totp step: %w", err)
		}
		if token, err = s.rotateSession(ctx, tx, sess.ID, StateAuthenticated, sessionMaxTTL); err != nil {
			return err
		}
		return s.recordLogin(ctx, tx, c, sess.ID, sess.UserID, methodPasswordTOTP)
	})
	if errors.Is(err, ErrInvalidCode) {
		s.totpFailed(ctx, c, sess, key)
		return "", ErrInvalidCode
	}
	if err != nil {
		return "", err
	}
	if err := s.clearThrottle(ctx, key); err != nil {
		// The login itself succeeded; a stale counter only expires later.
		s.log.Warn("clear totp throttle", "error", err)
	}
	return token, nil
}

// totpFailed records a wrong code. When the limit is reached, the partial
// session is revoked: the attacker has to get through the password again.
func (s *Service) totpFailed(ctx context.Context, c Client, sess Session, key string) {
	s.auditBestEffort(ctx, c, auditEntry{event: EventTOTPFailed, subject: &sess.UserID})
	locked, err := s.recordFailure(ctx, throttleTOTP, key)
	if err != nil {
		s.log.Error("throttle bookkeeping failed", "error", err)
		return
	}
	if locked {
		if _, err := s.db.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1`, sess.ID); err != nil {
			s.log.Error("revoke session after totp lockout", "error", err)
		}
	}
}

// Activate redeems an activation link: it sets the password and returns a
// partial session in which the user must enrol a second factor. The link
// itself is only marked used once enrolment succeeds.
func (s *Service) Activate(ctx context.Context, c Client, activationToken, password string) (string, error) {
	ipKey := throttleIP.key(c.IP)
	if err := s.checkThrottle(ctx, ipKey); err != nil {
		return "", err
	}

	hash := hashToken(activationToken)
	var userID uuid.UUID
	var username string
	err := s.db.QueryRow(ctx, `
		SELECT u.id, u.username FROM identity.activation_tokens a
		JOIN identity.users u ON u.id = a.user_id
		WHERE a.token_hash = $1 AND a.used_at IS NULL AND a.expires_at > $2 AND u.disabled_at IS NULL`,
		hash, s.now()).Scan(&userID, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		s.recordFailures(ctx, map[string]throttleRule{ipKey: throttleIP})
		return "", ErrInvalidToken
	}
	if err != nil {
		return "", fmt.Errorf("load activation token: %w", err)
	}
	if err := checkPasswordPolicy(password, username); err != nil {
		return "", err
	}
	passwordHash, err := s.hasher.hash(ctx, password)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	var token string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash = $2, updated_at = $3 WHERE id = $1`,
			userID, passwordHash, s.now()); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if token, _, err = s.createSession(ctx, tx, userID, StateMFAEnrollmentRequired, hash, c, partialSessionTTL); err != nil {
			return err
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventActivationStarted, actor: &userID})
	})
	return token, err
}

// TOTPEnrollment is what an authenticator app needs to add the account.
type TOTPEnrollment struct {
	Secret string `json:"secret"` // base32, for manual entry
	URI    string `json:"uri"`    // otpauth:// link
	QR     string `json:"qr"`     // the URI as a PNG QR code, data: URI
}

// BeginTOTPEnrollment generates a new, unconfirmed TOTP secret. Calling it
// again replaces a pending secret; a confirmed one is never overwritten.
func (s *Service) BeginTOTPEnrollment(ctx context.Context, sess Session, username string) (TOTPEnrollment, error) {
	if sess.State != StateMFAEnrollmentRequired {
		return TOTPEnrollment{}, ErrWrongState
	}
	secret := newTOTPSecret()
	tag, err := s.db.Exec(ctx, `
		INSERT INTO identity.totp_credentials AS t (user_id, secret_sealed, created_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET secret_sealed = EXCLUDED.secret_sealed, created_at = EXCLUDED.created_at
		WHERE t.confirmed_at IS NULL`,
		sess.UserID, s.sealer.Seal(secret, totpAD(sess.UserID)), s.now())
	if err != nil {
		return TOTPEnrollment{}, fmt.Errorf("store totp secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return TOTPEnrollment{}, ErrWrongState
	}
	uri := totpURI("Cloud", username, secret)
	qrCode, err := totpQR(uri)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	return TOTPEnrollment{Secret: base32NoPad.EncodeToString(secret), URI: uri, QR: qrCode}, nil
}

// ConfirmTOTPEnrollment checks the first code from the authenticator app. On
// success the second factor is active, the activation link is used up, and
// the session becomes a full session under a new token.
func (s *Service) ConfirmTOTPEnrollment(ctx context.Context, c Client, sess Session, code string) (string, error) {
	if sess.State != StateMFAEnrollmentRequired {
		return "", ErrWrongState
	}
	key := throttleTOTP.key(sess.UserID.String())
	if err := s.checkThrottle(ctx, key); err != nil {
		return "", err
	}

	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		now := s.now()
		var sealed []byte
		err := tx.QueryRow(ctx, `
			SELECT secret_sealed FROM identity.totp_credentials
			WHERE user_id = $1 AND confirmed_at IS NULL FOR UPDATE`, sess.UserID).Scan(&sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrWrongState
		}
		if err != nil {
			return fmt.Errorf("load pending totp: %w", err)
		}
		secret, err := s.sealer.Open(sealed, totpAD(sess.UserID))
		if err != nil {
			return fmt.Errorf("open totp secret: %w", err)
		}
		step, ok := verifyTOTP(secret, code, now, 0)
		if !ok {
			return ErrInvalidCode
		}

		// The link must still be valid: if an admin issued a new one in the
		// meantime, this enrolment is void.
		tag, err := tx.Exec(ctx, `
			UPDATE identity.activation_tokens SET used_at = $2
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2`, sess.activationTokenHash, now)
		if err != nil {
			return fmt.Errorf("use activation token: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidToken
		}
		if _, err := tx.Exec(ctx, `
			UPDATE identity.totp_credentials SET confirmed_at = $2, last_used_step = $3 WHERE user_id = $1`,
			sess.UserID, now, step); err != nil {
			return fmt.Errorf("confirm totp: %w", err)
		}
		if token, err = s.rotateSession(ctx, tx, sess.ID, StateAuthenticated, sessionMaxTTL); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, c, auditEntry{event: EventTOTPEnrolled, actor: &sess.UserID}); err != nil {
			return err
		}
		return s.recordLogin(ctx, tx, c, sess.ID, sess.UserID, methodActivation)
	})
	if errors.Is(err, ErrInvalidCode) {
		s.totpFailed(ctx, c, sess, key)
		return "", ErrInvalidCode
	}
	if err != nil {
		return "", err
	}
	if err := s.clearThrottle(ctx, key); err != nil {
		// The login itself succeeded; a stale counter only expires later.
		s.log.Warn("clear totp throttle", "error", err)
	}
	return token, nil
}

// ChangePassword sets a new password after re-checking the current one, and
// signs out every other session of the user.
func (s *Service) ChangePassword(ctx context.Context, c Client, sess Session, current, next string) error {
	if sess.State != StateAuthenticated {
		return ErrWrongState
	}
	key := throttleUser.key(sess.UserID.String())
	if err := s.checkThrottle(ctx, key); err != nil {
		return err
	}
	var username, hash string
	if err := s.db.QueryRow(ctx, `SELECT username, password_hash FROM identity.users WHERE id = $1`,
		sess.UserID).Scan(&username, &hash); err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	if len(current) > maxPasswordBytes {
		return ErrInvalidCredentials
	}
	match, _, err := s.hasher.verify(ctx, current, hash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !match {
		s.recordFailures(ctx, map[string]throttleRule{key: throttleUser})
		return ErrInvalidCredentials
	}
	if err := checkPasswordPolicy(next, username); err != nil {
		return err
	}
	newHash, err := s.hasher.hash(ctx, next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash = $2, updated_at = $3 WHERE id = $1`,
			sess.UserID, newHash, s.now()); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1 AND id <> $2`, sess.UserID, sess.ID); err != nil {
			return fmt.Errorf("revoke other sessions: %w", err)
		}
		// Apps signed in through OIDC must sign in again too.
		if err := s.revokeAppAccess(ctx, tx, sess.UserID, nil, ""); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, c, auditEntry{event: EventPasswordChanged, actor: &sess.UserID}); err != nil {
			return err
		}
		return s.notify(ctx, tx, c, sess.UserID, noticePasswordChanged)
	})
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, c Client, sess Session) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1`, sess.ID); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventLogout, actor: &sess.UserID})
	})
}

// Sign-in methods, and how each is reported to apps: amr lists the methods
// used (RFC 8176), acr their strength (OpenID EAP ACR values: phr and phrh
// are phishing-resistant, phrh with a hardware-bound key).
const (
	methodPasswordTOTP     = "password+totp"
	methodActivation       = "activation"
	methodPasswordRecovery = "password+recovery_code"
	methodPasskey          = "passkey"          // synced passkey
	methodPasskeyHardware  = "passkey-hardware" // bound to one device

	acrMFA                       = "mfa"
	acrPhishingResistant         = "phr"
	acrPhishingResistantHardware = "phrh"
)

var authMethods = map[string]struct {
	amr []string
	acr string
}{
	methodPasswordTOTP:     {[]string{"pwd", "otp", "mfa"}, acrMFA},
	methodActivation:       {[]string{"pwd", "otp", "mfa"}, acrMFA},
	methodPasswordRecovery: {[]string{"pwd", "otp", "mfa"}, acrMFA},
	methodPasskey:          {[]string{"swk", "user", "mfa"}, acrPhishingResistant},
	methodPasskeyHardware:  {[]string{"hwk", "user", "mfa"}, acrPhishingResistantHardware},
}

// recordLogin marks a completed sign-in: it notes on the session how the
// user signed in, updates last_login_at and writes the audit event. Every
// way of signing in ends here.
func (s *Service) recordLogin(ctx context.Context, tx dbtx, c Client, sessionID, userID uuid.UUID, method string) error {
	m, ok := authMethods[method]
	if !ok {
		return fmt.Errorf("unknown sign-in method %q", method)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET amr = $2, acr = $3 WHERE id = $1`, sessionID, m.amr, m.acr); err != nil {
		return fmt.Errorf("record login: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET last_login_at = $2 WHERE id = $1`, userID, s.now()); err != nil {
		return fmt.Errorf("record login: %w", err)
	}
	return s.audit(ctx, tx, c, auditEntry{event: EventLoginSucceeded, actor: &userID, details: map[string]any{"method": method}})
}

// totpAD is the associated data binding a sealed TOTP secret to its owner.
func totpAD(userID uuid.UUID) []byte { return []byte("totp:" + userID.String()) }

// createSession stores a new session and returns its token.
func (s *Service) createSession(ctx context.Context, q dbtx, userID uuid.UUID, state SessionState, activationHash []byte, c Client, ttl time.Duration) (string, uuid.UUID, error) {
	token, hash := newToken()
	id := newID()
	now := s.now()
	_, err := q.Exec(ctx, `
		INSERT INTO identity.sessions
			(id, token_hash, user_id, state, activation_token_hash, site_id, ip, user_agent, created_at, last_seen_at, expires_at, authenticated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, CASE WHEN $4 = 'authenticated' THEN $9::timestamptz END)`,
		id, hash, userID, state, activationHash, s.opts.SiteID, c.IP, c.UserAgent, now, now.Add(ttl))
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("create session: %w", err)
	}
	return token, id, nil
}

// rotateSession gives a session a new token and state. Issuing a new token
// whenever privileges change prevents session fixation: a token an attacker
// planted or observed before login is worthless afterwards.
func (s *Service) rotateSession(ctx context.Context, q dbtx, id uuid.UUID, state SessionState, ttl time.Duration) (string, error) {
	token, hash := newToken()
	now := s.now()
	tag, err := q.Exec(ctx, `
		UPDATE identity.sessions
		SET token_hash = $2, state = $3, activation_token_hash = NULL, last_seen_at = $4, expires_at = $5,
		    authenticated_at = CASE WHEN $3 = 'authenticated' THEN $4::timestamptz END
		WHERE id = $1`, id, hash, state, now, now.Add(ttl))
	if err != nil {
		return "", fmt.Errorf("rotate session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", ErrNoSession
	}
	return token, nil
}

// Authenticate resolves a session token. Partial sessions are returned too;
// callers decide which states they accept.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, User, error) {
	if token == "" || len(token) > 64 {
		return Session{}, User{}, ErrNoSession
	}
	var sess Session
	var u User
	var lastSeen time.Time
	var authenticatedAt *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.state, s.expires_at, s.last_seen_at, s.activation_token_hash, s.authenticated_at, s.amr, s.acr,
		       u.username, u.display_name, COALESCE(u.email, '')
		FROM identity.sessions s JOIN identity.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND u.disabled_at IS NULL`, hashToken(token)).Scan(
		&sess.ID, &sess.UserID, &sess.State, &sess.ExpiresAt, &lastSeen, &sess.activationTokenHash, &authenticatedAt, &sess.AMR, &sess.ACR,
		&u.Username, &u.DisplayName, &u.Email)
	if authenticatedAt != nil {
		sess.AuthenticatedAt = *authenticatedAt
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrNoSession
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("load session: %w", err)
	}
	u.ID = sess.UserID

	now := s.now()
	if !now.Before(sess.ExpiresAt) || (sess.State == StateAuthenticated && now.Sub(lastSeen) >= sessionIdleTTL) {
		return Session{}, User{}, ErrNoSession
	}
	if now.Sub(lastSeen) >= lastSeenPrecision {
		if _, err := s.db.Exec(ctx, `UPDATE identity.sessions SET last_seen_at = $2 WHERE id = $1`, sess.ID, now); err != nil {
			s.log.Warn("update session last_seen", "error", err)
		}
	}
	return sess, u, nil
}
