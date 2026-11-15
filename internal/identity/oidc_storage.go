package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"github.com/zitadel/oidc/v3/pkg/op"
	"golang.org/x/text/language"
)

// scopeGroups requests the "groups" claim: the names of the user's groups.
const scopeGroups = "groups"

// oidcStorage implements the provider library's storage on top of the
// identity tables.
type oidcStorage struct {
	s *Service
}

var _ op.Storage = (*oidcStorage)(nil)

func sha(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// errAccessDenied is returned when a user may not (or no longer) use an app.
var errAccessDenied = errors.New("user may not use this app")

// --- authorization requests ---

// authRequest is a pending or completed authorization request.
type authRequest struct {
	id        uuid.UUID
	req       oidc.AuthRequest
	userID    *uuid.UUID
	authTime  time.Time
	createdAt time.Time
	amr       []string
	acr       string
}

var _ op.AuthRequest = (*authRequest)(nil)

func (a *authRequest) GetID() string                      { return a.id.String() }
func (a *authRequest) GetACR() string                     { return a.acr }
func (a *authRequest) GetAMR() []string                   { return a.amr }
func (a *authRequest) GetAudience() []string              { return []string{a.req.ClientID} }
func (a *authRequest) GetAuthTime() time.Time             { return a.authTime }
func (a *authRequest) GetClientID() string                { return a.req.ClientID }
func (a *authRequest) GetNonce() string                   { return a.req.Nonce }
func (a *authRequest) GetRedirectURI() string             { return a.req.RedirectURI }
func (a *authRequest) GetResponseType() oidc.ResponseType { return a.req.ResponseType }
func (a *authRequest) GetResponseMode() oidc.ResponseMode { return a.req.ResponseMode }
func (a *authRequest) GetScopes() []string                { return a.req.Scopes }
func (a *authRequest) GetState() string                   { return a.req.State }
func (a *authRequest) Done() bool                         { return a.userID != nil }

func (a *authRequest) GetSubject() string {
	if a.userID == nil {
		return ""
	}
	return a.userID.String()
}

func (a *authRequest) GetCodeChallenge() *oidc.CodeChallenge {
	if a.req.CodeChallenge == "" {
		return nil
	}
	return &oidc.CodeChallenge{Challenge: a.req.CodeChallenge, Method: a.req.CodeChallengeMethod}
}

func (st *oidcStorage) CreateAuthRequest(ctx context.Context, req *oidc.AuthRequest, _ string) (op.AuthRequest, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode auth request: %w", err)
	}
	a := &authRequest{id: newID(), req: *req}
	now := st.s.now()
	if _, err := st.s.db.Exec(ctx, `
		INSERT INTO identity.oidc_auth_requests (id, client_id, request, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, a.id, req.ClientID, raw, now, now.Add(oidcAuthRequestTTL)); err != nil {
		return nil, fmt.Errorf("store auth request: %w", err)
	}
	return a, nil
}

func (st *oidcStorage) loadAuthRequest(ctx context.Context, where string, arg any) (*authRequest, error) {
	a := &authRequest{}
	var raw []byte
	var authTime *time.Time
	err := st.s.db.QueryRow(ctx, `
		SELECT id, request, user_id, auth_time, created_at, amr, acr FROM identity.oidc_auth_requests
		WHERE `+where+` AND expires_at > $2`, arg, st.s.now()).Scan(&a.id, &raw, &a.userID, &authTime, &a.createdAt, &a.amr, &a.acr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, oidc.ErrInvalidRequest().WithDescription("authorization request not found or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("load auth request: %w", err)
	}
	if err := json.Unmarshal(raw, &a.req); err != nil {
		return nil, fmt.Errorf("decode auth request: %w", err)
	}
	if authTime != nil {
		a.authTime = authTime.UTC()
	}
	return a, nil
}

func (st *oidcStorage) AuthRequestByID(ctx context.Context, id string) (op.AuthRequest, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, oidc.ErrInvalidRequest().WithDescription("invalid authorization request id")
	}
	return st.loadAuthRequest(ctx, "id = $1", parsed)
}

// AuthRequestByCode looks up the request an authorization code belongs to.
// Codes are stored as hashes, like every other bearer secret.
func (st *oidcStorage) AuthRequestByCode(ctx context.Context, code string) (op.AuthRequest, error) {
	return st.loadAuthRequest(ctx, "code_hash = $1", sha(code))
}

func (st *oidcStorage) SaveAuthCode(ctx context.Context, id, code string) error {
	tag, err := st.s.db.Exec(ctx, `UPDATE identity.oidc_auth_requests SET code_hash = $2 WHERE id = $1 AND user_id IS NOT NULL`, id, sha(code))
	if err != nil {
		return fmt.Errorf("save auth code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return oidc.ErrInvalidRequest().WithDescription("authorization request not completed")
	}
	return nil
}

func (st *oidcStorage) DeleteAuthRequest(ctx context.Context, id string) error {
	if _, err := st.s.db.Exec(ctx, `DELETE FROM identity.oidc_auth_requests WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete auth request: %w", err)
	}
	return nil
}

// --- tokens ---

// refreshRequest carries a refresh token's grant into a token request.
type refreshRequest struct {
	id       uuid.UUID
	clientID string
	userID   uuid.UUID
	scopes   []string
	authTime time.Time
	amr      []string
	acr      string
}

var _ op.RefreshTokenRequest = (*refreshRequest)(nil)

func (r *refreshRequest) GetAMR() []string                 { return r.amr }
func (r *refreshRequest) GetAudience() []string            { return []string{r.clientID} }
func (r *refreshRequest) GetAuthTime() time.Time           { return r.authTime }
func (r *refreshRequest) GetClientID() string              { return r.clientID }
func (r *refreshRequest) GetScopes() []string              { return r.scopes }
func (r *refreshRequest) GetSubject() string               { return r.userID.String() }
func (r *refreshRequest) SetCurrentScopes(scopes []string) { r.scopes = scopes }

// grant is who gets a token for which app, and how they signed in.
type grant struct {
	clientID string
	userID   uuid.UUID
	authTime time.Time
	amr      []string
	acr      string
}

// grantOf extracts the grant from either kind of token request.
func grantOf(req op.TokenRequest) (grant, error) {
	switch r := req.(type) {
	case *authRequest:
		if r.userID == nil {
			return grant{}, oidc.ErrInvalidGrant()
		}
		return grant{r.req.ClientID, *r.userID, r.authTime, r.amr, r.acr}, nil
	case *refreshRequest:
		return grant{r.clientID, r.userID, r.authTime, r.amr, r.acr}, nil
	default:
		return grant{}, fmt.Errorf("unsupported token request %T", req)
	}
}

// mayUseClient checks that the account is active and, for a restricted app,
// still in the allowed group. It runs for every token issued, so disabling
// a user or removing them from the group takes effect at the next refresh.
func (s *Service) mayUseClient(ctx context.Context, q dbtx, userID uuid.UUID, clientID string) error {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT u.disabled_at IS NULL AND (c.allowed_group_id IS NULL OR EXISTS (
			SELECT 1 FROM identity.group_members m WHERE m.group_id = c.allowed_group_id AND m.user_id = u.id))
		FROM identity.users u, identity.oidc_clients c
		WHERE u.id = $1 AND c.id = $2`, userID, clientID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !ok) {
		return errAccessDenied
	}
	return err
}

func (st *oidcStorage) insertAccessToken(ctx context.Context, q dbtx, clientID string, userID uuid.UUID, scopes []string, refreshID *uuid.UUID) (string, time.Time, error) {
	id := newID()
	expires := st.s.now().Add(oidcAccessTokenTTL)
	if _, err := q.Exec(ctx, `
		INSERT INTO identity.oidc_access_tokens (id, client_id, user_id, scopes, refresh_token_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, clientID, userID, scopes, refreshID, expires); err != nil {
		return "", time.Time{}, fmt.Errorf("store access token: %w", err)
	}
	return id.String(), expires, nil
}

func (st *oidcStorage) CreateAccessToken(ctx context.Context, req op.TokenRequest) (string, time.Time, error) {
	g, err := grantOf(req)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := st.s.mayUseClient(ctx, st.s.db, g.userID, g.clientID); err != nil {
		return "", time.Time{}, oidc.ErrAccessDenied()
	}
	return st.insertAccessToken(ctx, st.s.db, g.clientID, g.userID, req.GetScopes(), nil)
}

// CreateAccessAndRefreshTokens issues tokens for the code flow (current is
// empty) or rotates a refresh token: the old one stops working, so a stolen
// refresh token is useless once the real app has used it.
func (st *oidcStorage) CreateAccessAndRefreshTokens(ctx context.Context, req op.TokenRequest, current string) (string, string, time.Time, error) {
	g, err := grantOf(req)
	if err != nil {
		return "", "", time.Time{}, err
	}
	clientID, userID := g.clientID, g.userID
	refresh, refreshHash := newToken()
	now := st.s.now()
	var accessID string
	var expires time.Time
	err = pgx.BeginFunc(ctx, st.s.db, func(tx pgx.Tx) error {
		if err := st.s.mayUseClient(ctx, tx, userID, clientID); err != nil {
			return err
		}
		refreshID := newID()
		if current == "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO identity.oidc_refresh_tokens (id, token_hash, client_id, user_id, scopes, auth_time, amr, acr, created_at, expires_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				refreshID, refreshHash, clientID, userID, req.GetScopes(), g.authTime, g.amr, g.acr, now, now.Add(oidcRefreshTokenTTL)); err != nil {
				return fmt.Errorf("store refresh token: %w", err)
			}
		} else {
			// Rotation: same grant, new secret; the access tokens issued
			// with the old refresh token are revoked.
			err := tx.QueryRow(ctx, `
				UPDATE identity.oidc_refresh_tokens SET token_hash = $3, scopes = $4, expires_at = $5
				WHERE token_hash = $1 AND client_id = $2 AND expires_at > $6
				RETURNING id`, sha(current), clientID, refreshHash, req.GetScopes(), now.Add(oidcRefreshTokenTTL), now).Scan(&refreshID)
			if errors.Is(err, pgx.ErrNoRows) {
				return oidc.ErrInvalidGrant()
			}
			if err != nil {
				return fmt.Errorf("rotate refresh token: %w", err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM identity.oidc_access_tokens WHERE refresh_token_id = $1`, refreshID); err != nil {
				return fmt.Errorf("revoke old access tokens: %w", err)
			}
		}
		accessID, expires, err = st.insertAccessToken(ctx, tx, clientID, userID, req.GetScopes(), &refreshID)
		return err
	})
	if errors.Is(err, errAccessDenied) {
		return "", "", time.Time{}, oidc.ErrAccessDenied()
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	return accessID, refresh, expires, nil
}

func (st *oidcStorage) TokenRequestByRefreshToken(ctx context.Context, token string) (op.RefreshTokenRequest, error) {
	r := &refreshRequest{}
	err := st.s.db.QueryRow(ctx, `
		SELECT id, client_id, user_id, scopes, auth_time, amr, acr FROM identity.oidc_refresh_tokens
		WHERE token_hash = $1 AND expires_at > $2`, sha(token), st.s.now()).
		Scan(&r.id, &r.clientID, &r.userID, &r.scopes, &r.authTime, &r.amr, &r.acr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, oidc.ErrInvalidGrant()
	}
	if err != nil {
		return nil, fmt.Errorf("load refresh token: %w", err)
	}
	if err := st.s.mayUseClient(ctx, st.s.db, r.userID, r.clientID); err != nil {
		return nil, oidc.ErrInvalidGrant().WithDescription("access to this app was revoked")
	}
	return r, nil
}

// RevokeToken implements RFC 7009 for access token IDs and refresh tokens.
func (st *oidcStorage) RevokeToken(ctx context.Context, tokenOrID, _ string, clientID string) *oidc.Error {
	if id, err := uuid.Parse(tokenOrID); err == nil {
		if _, err := st.s.db.Exec(ctx, `DELETE FROM identity.oidc_access_tokens WHERE id = $1 AND client_id = $2`, id, clientID); err != nil {
			return oidc.ErrServerError()
		}
		return nil
	}
	if _, err := st.s.db.Exec(ctx, `DELETE FROM identity.oidc_refresh_tokens WHERE token_hash = $1 AND client_id = $2`, sha(tokenOrID), clientID); err != nil {
		return oidc.ErrServerError()
	}
	return nil
}

func (st *oidcStorage) GetRefreshTokenInfo(ctx context.Context, clientID, token string) (string, string, error) {
	var userID, id uuid.UUID
	err := st.s.db.QueryRow(ctx, `SELECT user_id, id FROM identity.oidc_refresh_tokens WHERE token_hash = $1 AND client_id = $2`,
		sha(token), clientID).Scan(&userID, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", op.ErrInvalidRefreshToken
	}
	if err != nil {
		return "", "", fmt.Errorf("load refresh token: %w", err)
	}
	return userID.String(), id.String(), nil
}

// --- keys ---

func (st *oidcStorage) SigningKey(ctx context.Context) (op.SigningKey, error) {
	keys, err := st.s.publishedKeys(ctx)
	if err == nil && len(keys) == 0 {
		err = errors.New("no OIDC signing key")
	}
	if err != nil {
		return nil, err
	}
	return keys[0], nil
}

func (st *oidcStorage) SignatureAlgorithms(context.Context) ([]jose.SignatureAlgorithm, error) {
	return []jose.SignatureAlgorithm{jose.RS256}, nil
}

func (st *oidcStorage) KeySet(ctx context.Context) ([]op.Key, error) {
	keys, err := st.s.publishedKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]op.Key, len(keys))
	for i, k := range keys {
		out[i] = publicKey{id: k.id, key: &k.key.PublicKey}
	}
	return out, nil
}

// --- clients ---

func (st *oidcStorage) GetClientByClientID(ctx context.Context, clientID string) (op.Client, error) {
	c, err := st.s.loadClient(ctx, clientID)
	if errors.Is(err, ErrNotFound) {
		return nil, oidc.ErrInvalidClient().WithDescription("unknown client")
	}
	if err != nil {
		return nil, err
	}
	return &opClient{storedClient: c, devMode: st.s.opts.DevMode, loginURL: st.s.opts.PublicURL.String() + "/connect?request="}, nil
}

func (st *oidcStorage) AuthorizeClientIDSecret(ctx context.Context, clientID, secret string) error {
	c, err := st.s.loadClient(ctx, clientID)
	if err != nil || !(&opClient{storedClient: c}).secretMatches(secret) {
		return oidc.ErrInvalidClient().WithDescription("invalid client credentials")
	}
	return nil
}

// --- claims ---

// setUserinfo fills the claims for the requested scopes.
func (s *Service) setUserinfo(ctx context.Context, info *oidc.UserInfo, userID uuid.UUID, scopes []string) error {
	var u User
	var p Profile
	var avatarKey *string
	var updatedAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT username, display_name, COALESCE(email, ''), updated_at, given_name, family_name, locale, avatar_key
		FROM identity.users WHERE id = $1 AND disabled_at IS NULL`, userID).
		Scan(&u.Username, &u.DisplayName, &u.Email, &updatedAt, &p.GivenName, &p.FamilyName, &p.Locale, &avatarKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return oidc.ErrAccessDenied()
	}
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	info.Subject = userID.String()
	for _, scope := range scopes {
		switch scope {
		case oidc.ScopeProfile:
			info.Name = u.DisplayName
			info.PreferredUsername = u.Username
			info.GivenName = p.GivenName
			info.FamilyName = p.FamilyName
			info.Picture = s.avatarURL(avatarKey)
			if tag, err := language.Parse(p.Locale); err == nil {
				info.Locale = oidc.NewLocale(tag)
			}
			info.UpdatedAt = oidc.FromTime(updatedAt)
		case oidc.ScopeEmail:
			if u.Email != "" {
				info.Email = u.Email
				// Only administrators set addresses (ADR 0007), so they are
				// treated as verified.
				info.EmailVerified = true
			}
		case scopeGroups:
			groups, err := s.UserGroups(ctx, userID)
			if err != nil {
				return err
			}
			names := make([]string, len(groups))
			for i, g := range groups {
				names[i] = g.Name
			}
			info.AppendClaims("groups", names)
		}
	}
	return nil
}

func (st *oidcStorage) SetUserinfoFromScopes(ctx context.Context, info *oidc.UserInfo, userID, _ string, scopes []string) error {
	id, err := uuid.Parse(userID)
	if err != nil {
		return oidc.ErrInvalidRequest()
	}
	return st.s.setUserinfo(ctx, info, id, scopes)
}

// accessToken loads a valid access token.
func (st *oidcStorage) accessToken(ctx context.Context, tokenID string) (clientID string, userID uuid.UUID, scopes []string, expires time.Time, err error) {
	id, err := uuid.Parse(tokenID)
	if err != nil {
		return "", uuid.Nil, nil, time.Time{}, oidc.ErrInvalidRequest()
	}
	err = st.s.db.QueryRow(ctx, `
		SELECT client_id, user_id, scopes, expires_at FROM identity.oidc_access_tokens
		WHERE id = $1 AND expires_at > $2`, id, st.s.now()).Scan(&clientID, &userID, &scopes, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", uuid.Nil, nil, time.Time{}, oidc.ErrAccessDenied().WithDescription("token expired or revoked")
	}
	return clientID, userID, scopes, expires, err
}

func (st *oidcStorage) SetUserinfoFromToken(ctx context.Context, info *oidc.UserInfo, tokenID, _, _ string) error {
	clientID, userID, scopes, _, err := st.accessToken(ctx, tokenID)
	if err != nil {
		return err
	}
	if err := st.s.mayUseClient(ctx, st.s.db, userID, clientID); err != nil {
		return oidc.ErrAccessDenied()
	}
	return st.s.setUserinfo(ctx, info, userID, scopes)
}

func (st *oidcStorage) SetIntrospectionFromToken(ctx context.Context, resp *oidc.IntrospectionResponse, tokenID, _, introspectingClient string) error {
	clientID, userID, scopes, expires, err := st.accessToken(ctx, tokenID)
	if err != nil || clientID != introspectingClient {
		return nil // an inactive token: Active stays false
	}
	info := new(oidc.UserInfo)
	if err := st.s.setUserinfo(ctx, info, userID, scopes); err != nil {
		return nil
	}
	resp.SetUserInfo(info)
	resp.Active = true
	resp.Scope = scopes
	resp.ClientID = clientID
	resp.Expiration = oidc.FromTime(expires)
	return nil
}

func (st *oidcStorage) GetPrivateClaimsFromScopes(context.Context, string, string, []string) (map[string]any, error) {
	return nil, nil
}

// JWT profile grants (RFC 7523) are not supported.
func (st *oidcStorage) GetKeyByIDAndClientID(context.Context, string, string) (*jose.JSONWebKey, error) {
	return nil, oidc.ErrUnauthorizedClient()
}

func (st *oidcStorage) ValidateJWTProfileScopes(context.Context, string, []string) ([]string, error) {
	return nil, oidc.ErrUnauthorizedClient()
}

func (st *oidcStorage) Health(ctx context.Context) error { return st.s.db.Ping(ctx) }
