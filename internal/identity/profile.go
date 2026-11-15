package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// ObjectStore is the part of the S3 client the identity module uses.
type ObjectStore interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

const avatarPrefix = "identity/avatars/"

// avatarFile is the public name of an avatar: 128 random bits, so the URL
// cannot be guessed even though serving it needs no sign-in.
var avatarFile = regexp.MustCompile(`^[0-9a-f]{32}\.jpg$`)

// Profile holds what users maintain about themselves.
type Profile struct {
	DisplayName string `json:"display_name"`
	GivenName   string `json:"given_name"`
	FamilyName  string `json:"family_name"`
	Locale      string `json:"locale"`  // "nl" or "en"
	Picture     string `json:"picture"` // absolute URL, empty without avatar
}

func (s *Service) avatarURL(key *string) string {
	if key == nil {
		return ""
	}
	return s.opts.PublicURL.String() + "/avatars/" + strings.TrimPrefix(*key, avatarPrefix)
}

// Profile returns a user's profile.
func (s *Service) Profile(ctx context.Context, userID uuid.UUID) (Profile, error) {
	var p Profile
	var key *string
	err := s.db.QueryRow(ctx, `SELECT display_name, given_name, family_name, locale, avatar_key FROM identity.users WHERE id = $1`,
		userID).Scan(&p.DisplayName, &p.GivenName, &p.FamilyName, &p.Locale, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load profile: %w", err)
	}
	p.Picture = s.avatarURL(key)
	return p, nil
}

func validateName(s, what string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 100 || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return "", &InputError{fmt.Sprintf("%s mag maximaal 100 tekens zijn, zonder speciale tekens.", what)}
	}
	return s, nil
}

// UpdateProfile changes the user's own names and language.
func (s *Service) UpdateProfile(ctx context.Context, c Client, sess Session, p Profile) error {
	if sess.State != StateAuthenticated {
		return ErrWrongState
	}
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if err := validateDisplayName(p.DisplayName); err != nil {
		return err
	}
	var err error
	if p.GivenName, err = validateName(p.GivenName, "De voornaam"); err != nil {
		return err
	}
	if p.FamilyName, err = validateName(p.FamilyName, "De achternaam"); err != nil {
		return err
	}
	if p.Locale != "nl" && p.Locale != "en" {
		return &InputError{"Kies Nederlands of Engels."}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.users SET display_name = $2, given_name = $3, family_name = $4, locale = $5, updated_at = $6
			WHERE id = $1`, sess.UserID, p.DisplayName, p.GivenName, p.FamilyName, p.Locale, s.now()); err != nil {
			return fmt.Errorf("update profile: %w", err)
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventProfileUpdated, actor: &sess.UserID})
	})
}

// SetAvatar stores a new profile picture; nil data removes it. The old
// object is deleted once the database points at the new one.
func (s *Service) SetAvatar(ctx context.Context, c Client, sess Session, data []byte) error {
	if sess.State != StateAuthenticated {
		return ErrWrongState
	}
	var newKey *string
	if data != nil {
		jpg, err := processAvatar(data)
		if err != nil {
			return err
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		key := avatarPrefix + hex.EncodeToString(b) + ".jpg"
		if err := s.opts.Objects.Put(ctx, key, jpg, "image/jpeg"); err != nil {
			return fmt.Errorf("store avatar: %w", err)
		}
		newKey = &key
	}
	var oldKey *string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT avatar_key FROM identity.users WHERE id = $1 FOR UPDATE`, sess.UserID).Scan(&oldKey); err != nil {
			return fmt.Errorf("load avatar: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.users SET avatar_key = $2, updated_at = $3 WHERE id = $1`, sess.UserID, newKey, s.now()); err != nil {
			return fmt.Errorf("update avatar: %w", err)
		}
		change := "removed"
		if newKey != nil {
			change = "changed"
		}
		return s.audit(ctx, tx, c, auditEntry{event: EventProfileUpdated, actor: &sess.UserID, details: map[string]any{"avatar": change}})
	})
	// Whichever object is no longer referenced is removed; a failure only
	// leaves an orphan that nobody can reach.
	orphan := oldKey
	if err != nil {
		orphan = newKey
	}
	if orphan != nil {
		if delErr := s.opts.Objects.Delete(context.WithoutCancel(ctx), *orphan); delErr != nil {
			s.log.Warn("delete unused avatar", "key", *orphan, "error", delErr)
		}
	}
	return err
}

// AvatarHandler serves profile pictures at /avatars/{file}. They are public
// by design: apps show them via the picture claim, without a session here.
func (s *Service) AvatarHandler(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	var exists bool
	if avatarFile.MatchString(file) {
		if err := s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM identity.users WHERE avatar_key = $1 AND disabled_at IS NULL)`,
			avatarPrefix+file).Scan(&exists); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	data, err := s.opts.Objects.Get(r.Context(), avatarPrefix+file)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "public, max-age=31536000, immutable") // the key never gets new content
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")         // other apps may show it
	_, _ = w.Write(data)
}

func (s *Service) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	profile, err := s.Profile(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, profile)
}

func (s *Service) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"display_name"`
		GivenName   string `json:"given_name"`
		FamilyName  string `json:"family_name"`
		Locale      string `json:"locale"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	err := s.UpdateProfile(r.Context(), clientFrom(r), p.Session,
		Profile{DisplayName: in.DisplayName, GivenName: in.GivenName, FamilyName: in.FamilyName, Locale: in.Locale})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleGetProfile(w, r)
}

// handleSetAvatar takes the raw image as the request body.
func (s *Service) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "image/png" && mediaType != "image/jpeg" {
		httpserver.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Gebruik een PNG- of JPEG-afbeelding.")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAvatarUpload))
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		httpserver.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "De afbeelding is groter dan 5 MB.")
		return
	}
	if err != nil {
		s.log.Warn("read avatar upload", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "De upload kon niet worden gelezen.")
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := s.SetAvatar(r.Context(), clientFrom(r), p.Session, data); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleGetProfile(w, r)
}

func (s *Service) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := s.SetAvatar(r.Context(), clientFrom(r), p.Session, nil); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleGetProfile(w, r)
}
