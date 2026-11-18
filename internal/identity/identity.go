// Package identity owns accounts, authentication, sessions and the security
// audit log. Other modules use it only through Service and its middleware.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/zitadel/oidc/v3/pkg/op"

	"github.com/AlbertovanEckeveld/cloud/internal/core/mailer"
	"github.com/AlbertovanEckeveld/cloud/internal/core/sealer"
)

// Lifetimes of credentials issued by this module.
const (
	partialSessionTTL = 10 * time.Minute    // between password and second factor
	sessionIdleTTL    = 7 * 24 * time.Hour  // a full session expires after a week without use
	sessionMaxTTL     = 30 * 24 * time.Hour // and always after 30 days
	activationTTL     = 72 * time.Hour
	lastSeenPrecision = time.Minute // avoid a database write on every request
	// recentAuthWindow is how long after signing in sensitive account changes
	// (passkeys, recovery codes) are allowed without signing in again. A
	// stolen session cookie then cannot be turned into lasting access.
	recentAuthWindow = 15 * time.Minute
)

// SessionState tells how far a session got through authentication.
type SessionState string

const (
	StateMFARequired           SessionState = "mfa_required"
	StateMFAEnrollmentRequired SessionState = "mfa_enrollment_required"
	StateAuthenticated         SessionState = "authenticated"
)

// Errors returned to the HTTP layer. Their messages are not shown to users.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrNotActivated       = errors.New("account not activated")
	ErrInvalidCode        = errors.New("invalid verification code")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrNoSession          = errors.New("no valid session")
	ErrWrongState         = errors.New("session is in the wrong state for this action")
	ErrUserExists         = errors.New("username already taken")
	ErrUserNotFound       = errors.New("user not found")
	ErrReauthRequired     = errors.New("recent authentication required")
	ErrNotFound           = errors.New("not found")
	ErrPasskeyRejected    = errors.New("passkey rejected")
	ErrChallengeInvalid   = errors.New("webauthn challenge unknown, used or expired")
	ErrEmailExists        = errors.New("e-mail address already in use")
)

// User is an account.
type User struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"` // empty when unknown
}

// Session is a browser session as stored in the database.
type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	State     SessionState
	ExpiresAt time.Time
	// AuthenticatedAt is when the user last proved their identity in this
	// session; zero for partial sessions.
	AuthenticatedAt time.Time
	// AMR and ACR describe how the user signed in (see recordLogin).
	AMR []string
	ACR string

	activationTokenHash []byte
}

// Client describes who sent a request, for throttling and the audit log.
type Client struct {
	IP        string
	UserAgent string
}

// Options configures the service.
type Options struct {
	SiteID    string
	PublicURL *url.URL
	// ExtraOrigins are additional origins allowed for passkey ceremonies,
	// such as the Vite dev server during development.
	ExtraOrigins []string
	// Mailer sends activation links and security notifications; it may be
	// a disabled mailer, but never nil.
	Mailer *mailer.Mailer
	// OIDCTokenKey encrypts the OIDC provider's opaque access tokens.
	OIDCTokenKey [32]byte
	// Objects stores profile pictures.
	Objects ObjectStore
	// DevMode allows plain-http redirect URIs for OIDC apps.
	DevMode bool
	Logger  *slog.Logger
}

// Service implements the identity module.
type Service struct {
	db       *pgxpool.Pool
	sealer   *sealer.Sealer
	hasher   *passwordHasher
	webauthn *webauthn.WebAuthn
	mail     *mailer.Mailer
	// oidcProvider is set by OIDCHandler; used to answer denied requests.
	oidcProvider op.OpenIDProvider
	opts         Options
	log          *slog.Logger
	now          func() time.Time
}

// New returns the identity service.
func New(db *pgxpool.Pool, s *sealer.Sealer, opts Options) (*Service, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          opts.PublicURL.Hostname(),
		RPDisplayName: "Cloud",
		RPOrigins:     append([]string{opts.PublicURL.String()}, opts.ExtraOrigins...),
	})
	if err != nil {
		return nil, fmt.Errorf("configure webauthn: %w", err)
	}
	return &Service{
		db:       db,
		sealer:   s,
		webauthn: wa,
		mail:     opts.Mailer,
		// Two concurrent hashes cap Argon2id memory use at 128 MiB.
		hasher: newPasswordHasher(defaultArgonParams, 2),
		opts:   opts,
		log:    opts.Logger,
		now:    time.Now,
	}, nil
}

// requireRecentAuth allows sensitive changes only shortly after signing in.
func (s *Service) requireRecentAuth(sess Session) error {
	if sess.State != StateAuthenticated || s.now().Sub(sess.AuthenticatedAt) > recentAuthWindow {
		return ErrReauthRequired
	}
	return nil
}

// dbtx is implemented by both the pool and a transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// newToken returns a 256-bit random token for cookies and links, and the
// SHA-256 hash under which it is stored.
func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// Run performs periodic cleanup and delivers logout tokens to apps until
// ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	go s.deliverLogouts(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.cleanup(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("identity cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cleanup deletes expired sessions, stale throttle counters and old activation tokens.
func (s *Service) cleanup(ctx context.Context) error {
	now := s.now()
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM identity.sessions WHERE expires_at < $1 OR (state = 'authenticated' AND last_seen_at < $2)`,
			[]any{now, now.Add(-sessionIdleTTL)}},
		{`DELETE FROM identity.throttle WHERE last_failure_at < $1 AND (locked_until IS NULL OR locked_until < $2)`,
			[]any{now.Add(-24 * time.Hour), now}},
		{`DELETE FROM identity.activation_tokens WHERE expires_at < $1`,
			[]any{now.Add(-7 * 24 * time.Hour)}},
		{`DELETE FROM identity.webauthn_challenges WHERE expires_at < $1`, []any{now}},
		{`DELETE FROM identity.oidc_auth_requests WHERE expires_at < $1`, []any{now}},
		{`DELETE FROM identity.oidc_access_tokens WHERE expires_at < $1`, []any{now}},
		{`DELETE FROM identity.oidc_refresh_tokens WHERE expires_at < $1`, []any{now}},
		{`DELETE FROM identity.oidc_logout_requests WHERE expires_at < $1`, []any{now}},
	}
	for _, st := range statements {
		if _, err := s.db.Exec(ctx, st.sql, st.args...); err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}
	}
	// Rotate the OIDC signing key when it is due.
	return s.ensureSigningKey(ctx)
}
