package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
)

// OIDC token lifetimes.
const (
	oidcAuthRequestTTL  = 10 * time.Minute
	oidcAccessTokenTTL  = 15 * time.Minute
	oidcIDTokenTTL      = 15 * time.Minute
	oidcRefreshTokenTTL = 30 * 24 * time.Hour
	maxRedirectURIs     = 10
)

// OIDCClient is an app registered to sign in through this platform.
type OIDCClient struct {
	ID   string `json:"id"` // the client_id
	Name string `json:"name"`
	// Confidential clients (server-side apps) authenticate with a secret;
	// public clients (mobile, desktop, single-page apps) must use PKCE.
	Confidential           bool      `json:"confidential"`
	RedirectURIs           []string  `json:"redirect_uris"`
	PostLogoutRedirectURIs []string  `json:"post_logout_redirect_uris"`
	AllowedGroup           *GroupRef `json:"allowed_group"`
	CreatedAt              time.Time `json:"created_at"`
}

// OIDCClientInput holds the editable fields of a client.
type OIDCClientInput struct {
	Name                   string
	Confidential           bool
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	AllowedGroupID         *uuid.UUID
}

// ErrGroupInUse is returned when deleting a group that restricts an app.
var ErrGroupInUse = errors.New("group restricts access to an app")

// validateRedirectURIs enforces exact, absolute URIs. HTTPS is required,
// except for loopback addresses (native apps, RFC 8252 §7.3) and, in
// development, any http URI.
func validateRedirectURIs(uris []string, allowHTTP bool, required bool) ([]string, error) {
	var out []string
	for _, raw := range uris {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		invalid := err != nil || u.Host == "" || u.Fragment != "" || u.User != nil || len(raw) > 2048
		if !invalid {
			loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
			switch u.Scheme {
			case "https":
			case "http":
				invalid = !loopback && !allowHTTP
			default:
				invalid = true
			}
		}
		if invalid {
			return nil, &InputError{fmt.Sprintf("Ongeldige redirect-URI %q: gebruik een volledige https-URL (http alleen voor localhost).", raw)}
		}
		out = append(out, raw)
	}
	if required && len(out) == 0 {
		return nil, &InputError{"Geef minstens één redirect-URI op."}
	}
	if len(out) > maxRedirectURIs {
		return nil, &InputError{fmt.Sprintf("Maximaal %d redirect-URI's.", maxRedirectURIs)}
	}
	return out, nil
}

func (s *Service) normalizeClientInput(in *OIDCClientInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 64 {
		return &InputError{"De naam moet 1 tot 64 tekens zijn."}
	}
	var err error
	if in.RedirectURIs, err = validateRedirectURIs(in.RedirectURIs, s.opts.DevMode, true); err != nil {
		return err
	}
	if in.PostLogoutRedirectURIs, err = validateRedirectURIs(in.PostLogoutRedirectURIs, s.opts.DevMode, false); err != nil {
		return err
	}
	if in.PostLogoutRedirectURIs == nil {
		in.PostLogoutRedirectURIs = []string{}
	}
	return nil
}

// newClientSecret returns a 256-bit secret and its SHA-256. A fast hash is
// fine here: unlike a password, the secret has full entropy.
func newClientSecret() (string, []byte) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(secret))
	return secret, sum[:]
}

const clientQuery = `
	SELECT c.id, c.name, c.secret_hash, c.redirect_uris, c.post_logout_redirect_uris, c.created_at, g.id, g.name
	FROM identity.oidc_clients c LEFT JOIN identity.groups g ON g.id = c.allowed_group_id`

type storedClient struct {
	OIDCClient
	secretHash []byte
}

func scanClient(row pgx.CollectableRow) (storedClient, error) {
	var c storedClient
	var groupID *uuid.UUID
	var groupName *string
	err := row.Scan(&c.ID, &c.Name, &c.secretHash, &c.RedirectURIs, &c.PostLogoutRedirectURIs, &c.CreatedAt, &groupID, &groupName)
	c.Confidential = c.secretHash != nil
	c.CreatedAt = c.CreatedAt.UTC()
	if groupID != nil {
		c.AllowedGroup = &GroupRef{ID: *groupID, Name: *groupName}
	}
	return c, err
}

// ListOIDCClients returns all registered apps.
func (s *Service) ListOIDCClients(ctx context.Context) ([]OIDCClient, error) {
	rows, err := s.db.Query(ctx, clientQuery+` ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	stored, err := pgx.CollectRows(rows, scanClient)
	if err != nil {
		return nil, fmt.Errorf("read clients: %w", err)
	}
	out := make([]OIDCClient, len(stored))
	for i, c := range stored {
		out[i] = c.OIDCClient
	}
	return out, nil
}

func (s *Service) loadClient(ctx context.Context, id string) (storedClient, error) {
	rows, err := s.db.Query(ctx, clientQuery+` WHERE c.id = $1`, id)
	if err != nil {
		return storedClient{}, fmt.Errorf("load client: %w", err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanClient)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedClient{}, ErrNotFound
	}
	if err != nil {
		return storedClient{}, fmt.Errorf("read client: %w", err)
	}
	return c, nil
}

// GetOIDCClient returns one registered app.
func (s *Service) GetOIDCClient(ctx context.Context, id string) (OIDCClient, error) {
	c, err := s.loadClient(ctx, id)
	return c.OIDCClient, err
}

// CreateOIDCClient registers an app. For a confidential client the secret is
// returned once; only its hash is kept.
func (s *Service) CreateOIDCClient(ctx context.Context, c Client, actor Session, in OIDCClientInput) (OIDCClient, string, error) {
	if err := s.requireRecentAuth(actor); err != nil {
		return OIDCClient{}, "", err
	}
	if err := s.normalizeClientInput(&in); err != nil {
		return OIDCClient{}, "", err
	}
	id := "app-" + strings.ReplaceAll(newID().String(), "-", "")[:20]
	var secret string
	var secretHash []byte
	if in.Confidential {
		secret, secretHash = newClientSecret()
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO identity.oidc_clients (id, name, secret_hash, redirect_uris, post_logout_redirect_uris, allowed_group_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, in.Name, secretHash, in.RedirectURIs, in.PostLogoutRedirectURIs, in.AllowedGroupID, s.now())
		if err != nil {
			return fmt.Errorf("create client: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventOIDCClientCreated, actor: &actor.UserID, details: map[string]any{"app": in.Name}})
	})
	if err != nil {
		return OIDCClient{}, "", err
	}
	created, err := s.GetOIDCClient(ctx, id)
	return created, secret, err
}

// UpdateOIDCClient changes an app's name, redirect URIs or allowed group.
// Whether it is confidential cannot change.
func (s *Service) UpdateOIDCClient(ctx context.Context, c Client, actor Session, id string, in OIDCClientInput) error {
	if err := s.requireRecentAuth(actor); err != nil {
		return err
	}
	if err := s.normalizeClientInput(&in); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE identity.oidc_clients
			SET name = $2, redirect_uris = $3, post_logout_redirect_uris = $4, allowed_group_id = $5
			WHERE id = $1`, id, in.Name, in.RedirectURIs, in.PostLogoutRedirectURIs, in.AllowedGroupID)
		if err != nil {
			return fmt.Errorf("update client: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventOIDCClientUpdated, actor: &actor.UserID, details: map[string]any{"app": in.Name}})
	})
}

// RotateOIDCClientSecret replaces a confidential client's secret.
func (s *Service) RotateOIDCClientSecret(ctx context.Context, c Client, actor Session, id string) (string, error) {
	if err := s.requireRecentAuth(actor); err != nil {
		return "", err
	}
	secret, hash := newClientSecret()
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `UPDATE identity.oidc_clients SET secret_hash = $2 WHERE id = $1 AND secret_hash IS NOT NULL RETURNING name`,
			id, hash).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("rotate secret: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventOIDCClientSecretRotated, actor: &actor.UserID, details: map[string]any{"app": name}})
	})
	return secret, err
}

// DeleteOIDCClient removes an app; its tokens stop working immediately.
func (s *Service) DeleteOIDCClient(ctx context.Context, c Client, actor Session, id string) error {
	if err := s.requireRecentAuth(actor); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM identity.oidc_clients WHERE id = $1 RETURNING name`, id).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete client: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventOIDCClientDeleted, actor: &actor.UserID, details: map[string]any{"app": name}})
	})
}

// opClient adapts a stored client to the provider library.
type opClient struct {
	storedClient
	devMode  bool
	loginURL string
}

var _ op.Client = (*opClient)(nil)

func (c *opClient) GetID() string                    { return c.ID }
func (c *opClient) RedirectURIs() []string           { return c.OIDCClient.RedirectURIs }
func (c *opClient) PostLogoutRedirectURIs() []string { return c.OIDCClient.PostLogoutRedirectURIs }
func (c *opClient) LoginURL(id string) string        { return c.loginURL + url.QueryEscape(id) }
func (c *opClient) ResponseTypes() []oidc.ResponseType {
	return []oidc.ResponseType{oidc.ResponseTypeCode}
}
func (c *opClient) GrantTypes() []oidc.GrantType {
	return []oidc.GrantType{oidc.GrantTypeCode, oidc.GrantTypeRefreshToken}
}
func (c *opClient) AccessTokenType() op.AccessTokenType { return op.AccessTokenTypeBearer }
func (c *opClient) IDTokenLifetime() time.Duration      { return oidcIDTokenTTL }
func (c *opClient) DevMode() bool                       { return c.devMode }
func (c *opClient) ClockSkew() time.Duration            { return 0 }

// IDTokenUserinfoClaimsAssertion puts profile, email and groups claims in
// the ID token too: many apps read them only from there.
func (c *opClient) IDTokenUserinfoClaimsAssertion() bool { return true }
func (c *opClient) IsScopeAllowed(scope string) bool     { return scope == scopeGroups }
func (c *opClient) RestrictAdditionalIdTokenScopes() func([]string) []string {
	return func(scopes []string) []string { return scopes }
}
func (c *opClient) RestrictAdditionalAccessTokenScopes() func([]string) []string {
	return func(scopes []string) []string { return scopes }
}

func (c *opClient) ApplicationType() op.ApplicationType {
	if c.Confidential {
		return op.ApplicationTypeWeb
	}
	return op.ApplicationTypeNative
}

func (c *opClient) AuthMethod() oidc.AuthMethod {
	if c.Confidential {
		return oidc.AuthMethodBasic
	}
	return oidc.AuthMethodNone
}

func (c *opClient) secretMatches(secret string) bool {
	sum := sha256.Sum256([]byte(secret))
	return c.secretHash != nil && subtle.ConstantTimeCompare(sum[:], c.secretHash) == 1
}
