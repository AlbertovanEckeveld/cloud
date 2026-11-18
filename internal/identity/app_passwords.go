package identity

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Audit events and notices for app passwords.
const (
	EventAppPasswordCreated = "app_password.created"
	EventAppPasswordRevoked = "app_password.revoked"
	EventAppPasswordFailed  = "app_password.failed"
)

var noticeAppPassword = notice{"Er is een app-wachtwoord aangemaakt",
	"Er is zojuist een app-wachtwoord voor je account aangemaakt. Daarmee kan een app (bijvoorbeeld een WebDAV-client) bij je bestanden. Was jij dit niet? Trek het dan in op je accountpagina."}

// lastUsedEvery limits how often last_used_at is written: protocol clients
// authenticate every request.
const lastUsedEvery = time.Minute

// AppPassword is an app password as its owner sees it; the password itself
// is only returned once, when it is created.
type AppPassword struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Password   string     `json:"password,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// newAppPassword returns a random password of 26 base32 letters and digits
// (130 bits) in groups of five, easy to type on a phone if needed.
func newAppPassword() string {
	b := make([]byte, 17)
	_, _ = rand.Read(b)
	raw := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:26]
	var groups []string
	for i := 0; i < len(raw); i += 5 {
		groups = append(groups, raw[i:min(i+5, len(raw))])
	}
	return strings.Join(groups, "-")
}

// appPasswordHash hashes a password as typed; dashes and case are ignored.
func appPasswordHash(password string) []byte {
	return hashToken(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(password), "-", "")))
}

// CreateAppPassword makes an app password; it needs a recent sign-in.
func (s *Service) CreateAppPassword(ctx context.Context, c Client, sess Session, name string) (AppPassword, error) {
	if err := s.requireRecentAuth(sess); err != nil {
		return AppPassword{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return AppPassword{}, &InputError{"Geef het app-wachtwoord een naam (max. 100 tekens), bijvoorbeeld de app of het apparaat."}
	}
	ap := AppPassword{ID: newID(), Name: name, Password: newAppPassword(), CreatedAt: s.now().UTC()}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.app_passwords (id, user_id, name, token_hash, created_at) VALUES ($1, $2, $3, $4, $5)`,
			ap.ID, sess.UserID, name, appPasswordHash(ap.Password), ap.CreatedAt); err != nil {
			return fmt.Errorf("create app password: %w", err)
		}
		if err := s.audit(ctx, tx, c, auditEntry{event: EventAppPasswordCreated, actor: &sess.UserID, details: map[string]any{"name": name}}); err != nil {
			return err
		}
		return s.notify(ctx, tx, c, sess.UserID, noticeAppPassword)
	})
	return ap, err
}

// ListAppPasswords lists a user's app passwords.
func (s *Service) ListAppPasswords(ctx context.Context, userID uuid.UUID) ([]AppPassword, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, created_at, last_used_at FROM identity.app_passwords WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list app passwords: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppPassword, error) {
		var ap AppPassword
		err := row.Scan(&ap.ID, &ap.Name, &ap.CreatedAt, &ap.LastUsedAt)
		ap.CreatedAt = ap.CreatedAt.UTC()
		if ap.LastUsedAt != nil {
			*ap.LastUsedAt = ap.LastUsedAt.UTC()
		}
		return ap, err
	})
}

// RevokeAppPassword deletes one of the user's app passwords.
func (s *Service) RevokeAppPassword(ctx context.Context, c Client, userID, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var name string
		err := tx.QueryRow(ctx, `DELETE FROM identity.app_passwords WHERE id = $1 AND user_id = $2 RETURNING name`, id, userID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke app password: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventAppPasswordRevoked, actor: &userID, details: map[string]any{"name": name}})
	})
}

// AuthenticateAppPassword checks a username and app password, as sent by a
// protocol client with HTTP Basic authentication. Failures count towards
// the same throttling as signing in.
func (s *Service) AuthenticateAppPassword(ctx context.Context, c Client, username, password string) (User, error) {
	username = normalizeUsername(username)
	userKey, ipKey := throttleUser.key(username), throttleIP.key(c.IP)
	if err := s.checkThrottle(ctx, userKey, ipKey); err != nil {
		return User{}, err
	}
	var u User
	var id uuid.UUID
	var lastUsed *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT a.id, a.last_used_at, u.id, u.username, u.display_name, COALESCE(u.email, '')
		FROM identity.app_passwords a JOIN identity.users u ON u.id = a.user_id
		WHERE a.token_hash = $1 AND u.username = $2 AND u.disabled_at IS NULL`,
		appPasswordHash(password), username).Scan(&id, &lastUsed, &u.ID, &u.Username, &u.DisplayName, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		s.recordFailures(ctx, map[string]throttleRule{userKey: throttleUser, ipKey: throttleIP})
		s.auditBestEffort(ctx, c, auditEntry{event: EventAppPasswordFailed, details: map[string]any{"username": username[:min(len(username), 64)]}})
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, fmt.Errorf("check app password: %w", err)
	}
	if now := s.now(); lastUsed == nil || now.Sub(*lastUsed) > lastUsedEvery {
		if _, err := s.db.Exec(ctx, `UPDATE identity.app_passwords SET last_used_at = $2 WHERE id = $1`, id, now); err != nil {
			s.log.Warn("record app password use", "error", err)
		}
	}
	return u, nil
}

// RequireAppPassword authenticates protocol clients (WebDAV and later
// CalDAV) with HTTP Basic: username and app password. Account passwords are
// never accepted here; they need a second factor. realm names the service.
func (s *Service) RequireAppPassword(realm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || len(password) > 100 {
				challenge(w, realm)
				return
			}
			u, err := s.AuthenticateAppPassword(r.Context(), clientFrom(r), username, password)
			if throttled, isThrottled := errors.AsType[*ThrottledError](err); isThrottled {
				w.Header().Set("Retry-After", fmt.Sprint(int(throttled.RetryAfter.Seconds())+1))
				http.Error(w, "too many attempts", http.StatusTooManyRequests)
				return
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidCredentials) {
					s.log.Error("app password check failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
				}
				challenge(w, realm)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: u})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func challenge(w http.ResponseWriter, realm string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`", charset="UTF-8"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

func (s *Service) handleListAppPasswords(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	list, err := s.ListAppPasswords(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		AppPasswords []AppPassword `json:"app_passwords"`
	}{list})
}

func (s *Service) handleCreateAppPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	ap, err := s.CreateAppPassword(r.Context(), clientFrom(r), p.Session, in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, ap)
}

func (s *Service) handleRevokeAppPassword(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, ErrNotFound)
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := s.RevokeAppPassword(r.Context(), clientFrom(r), p.User.ID, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
