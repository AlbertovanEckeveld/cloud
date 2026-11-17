package drive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

// Share-link errors, mapped to responses that reveal no more than needed.
var (
	ErrPasswordRequired = errors.New("link needs a password")
	ErrWrongPassword    = errors.New("wrong link password")
	ErrLinkLocked       = errors.New("link temporarily locked")
)

const (
	linkMaxFailures = 10
	linkLockout     = 15 * time.Minute
	minLinkPassword = 8
)

// Argon2id with the same cost as account passwords (RFC 9106, second
// recommended option). Hashing runs on a public endpoint, so at most two
// run at once: a flood of requests cannot exhaust memory.
var (
	argonSlots = make(chan struct{}, 2)
	b64        = base64.RawStdEncoding
)

const (
	argonTime, argonMemory, argonThreads, argonKeyLen = 3, 64 * 1024, 4, 32
)

func argonKey(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case argonSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen), nil
}

func hashLinkPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, err := argonKey(ctx, password, salt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func verifyLinkPassword(ctx context.Context, password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return false, errors.New("malformed link password hash")
	}
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false, errors.New("malformed link password hash")
	}
	key, err := argonKey(ctx, password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, want) == 1, nil
}

// Link is a public share link as its managers see it. The token itself is
// only returned once, when the link is created.
type Link struct {
	ID        uuid.UUID  `json:"id"`
	NodeID    uuid.UUID  `json:"node_id"`
	Token     string     `json:"token,omitempty"`
	Password  bool       `json:"password"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	Downloads int        `json:"downloads"`
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateLink makes a public, read-only link to a node; it needs manage.
func (s *Service) CreateLink(ctx context.Context, userID, nodeID uuid.UUID, expires *time.Time, password string) (Link, error) {
	if _, err := s.authorize(ctx, s.db, userID, nodeID, authz.Manage, false); err != nil {
		return Link{}, err
	}
	if expires != nil && !expires.After(s.now()) {
		return Link{}, &InputError{"De verloopdatum moet in de toekomst liggen."}
	}
	var hash *string
	if password != "" {
		if len([]rune(password)) < minLinkPassword {
			return Link{}, &InputError{"Kies een wachtwoord van minstens 8 tekens."}
		}
		h, err := hashLinkPassword(ctx, password)
		if err != nil {
			return Link{}, err
		}
		hash = &h
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	l := Link{ID: newID(), NodeID: nodeID, Token: base64.RawURLEncoding.EncodeToString(raw), Password: hash != nil,
		ExpiresAt: expires, CreatedAt: s.now().UTC()}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO drive.links (id, node_id, token_hash, password_hash, expires_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, l.ID, nodeID, tokenHash(l.Token), hash, expires, userID, l.CreatedAt); err != nil {
		return Link{}, fmt.Errorf("create link: %w", err)
	}
	return l, nil
}

// Links lists the links of a node; it needs manage.
func (s *Service) Links(ctx context.Context, userID, nodeID uuid.UUID) ([]Link, error) {
	if _, err := s.authorize(ctx, s.db, userID, nodeID, authz.Manage, false); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, node_id, password_hash IS NOT NULL, expires_at, created_at, downloads
		FROM drive.links WHERE node_id = $1 ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Link, error) {
		var l Link
		err := row.Scan(&l.ID, &l.NodeID, &l.Password, &l.ExpiresAt, &l.CreatedAt, &l.Downloads)
		l.CreatedAt = l.CreatedAt.UTC()
		if l.ExpiresAt != nil {
			*l.ExpiresAt = l.ExpiresAt.UTC()
		}
		return l, err
	})
}

// DeleteLink revokes a link; it needs manage on the linked node.
func (s *Service) DeleteLink(ctx context.Context, userID, linkID uuid.UUID) error {
	var nodeID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT node_id FROM drive.links WHERE id = $1`, linkID).Scan(&nodeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("load link: %w", err)
	}
	if _, err := s.authorize(ctx, s.db, userID, nodeID, authz.Manage, false); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM drive.links WHERE id = $1`, linkID); err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	return nil
}

// openLink checks a token (and password) and returns the linked node. Every
// failure that does not involve the password is ErrNotFound: an expired,
// revoked or trashed link looks the same as one that never existed.
func (s *Service) openLink(ctx context.Context, token, password string) (Node, uuid.UUID, error) {
	var linkID, nodeID uuid.UUID
	var hash *string
	var expires, locked *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT id, node_id, password_hash, expires_at, locked_until FROM drive.links WHERE token_hash = $1`,
		tokenHash(token)).Scan(&linkID, &nodeID, &hash, &expires, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, uuid.Nil, ErrNotFound
	}
	if err != nil {
		return Node{}, uuid.Nil, fmt.Errorf("load link: %w", err)
	}
	now := s.now()
	if expires != nil && !expires.After(now) {
		return Node{}, uuid.Nil, ErrNotFound
	}
	if hash != nil {
		if locked != nil && locked.After(now) {
			return Node{}, uuid.Nil, ErrLinkLocked
		}
		if password == "" {
			return Node{}, uuid.Nil, ErrPasswordRequired
		}
		ok, err := verifyLinkPassword(ctx, password, *hash)
		if err != nil {
			return Node{}, uuid.Nil, err
		}
		if !ok {
			if _, err := s.db.Exec(context.WithoutCancel(ctx), `
				UPDATE drive.links SET failed_attempts = failed_attempts + 1,
				       locked_until = CASE WHEN failed_attempts + 1 >= $2 THEN $3::timestamptz END
				WHERE id = $1`, linkID, linkMaxFailures, now.Add(linkLockout)); err != nil {
				return Node{}, uuid.Nil, fmt.Errorf("count failed attempt: %w", err)
			}
			return Node{}, uuid.Nil, ErrWrongPassword
		}
		if _, err := s.db.Exec(ctx, `UPDATE drive.links SET failed_attempts = 0, locked_until = NULL WHERE id = $1 AND failed_attempts > 0`, linkID); err != nil {
			return Node{}, uuid.Nil, fmt.Errorf("reset failed attempts: %w", err)
		}
	}
	rows, err := s.db.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1`, nodeID)
	if err != nil {
		return Node{}, uuid.Nil, fmt.Errorf("load node: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if err != nil {
		return Node{}, uuid.Nil, ErrNotFound
	}
	if trashed, err := s.inTrash(ctx, s.db, nodeID); err != nil || trashed {
		return Node{}, uuid.Nil, ErrNotFound
	}
	n.Permission, n.ParentID = authz.View, nil
	return n, linkID, nil
}

// within loads a live node inside a linked folder (or the linked node itself).
func (s *Service) within(ctx context.Context, link Node, nodeID uuid.UUID) (Node, error) {
	if nodeID == link.ID {
		return link, nil
	}
	path, trashed, err := s.lineage(ctx, s.db, nodeID)
	if err != nil || trashed || !slices.Contains(path, link.ID) {
		return Node{}, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1`, nodeID)
	if err != nil {
		return Node{}, fmt.Errorf("load node: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if err != nil {
		return Node{}, ErrNotFound
	}
	n.Permission = authz.View
	return n, nil
}

// PublicView is what a link shows: the linked node and, for a folder, the
// contents of the requested subfolder with the path from the linked folder.
type PublicView struct {
	Node     Node    `json:"node"`
	Folder   *Node   `json:"folder"`
	Path     []Crumb `json:"path"`
	Children []Node  `json:"children"`
}

// OpenLink returns what a link shows; folderID (optional) browses into a
// subfolder of a linked folder.
func (s *Service) OpenLink(ctx context.Context, token, password string, folderID *uuid.UUID) (PublicView, error) {
	link, _, err := s.openLink(ctx, token, password)
	if err != nil {
		return PublicView{}, err
	}
	view := PublicView{Node: link, Path: []Crumb{}, Children: []Node{}}
	if link.Kind != "folder" {
		return view, nil
	}
	folder := link
	if folderID != nil {
		if folder, err = s.within(ctx, link, *folderID); err != nil {
			return PublicView{}, err
		}
		if folder.Kind != "folder" {
			return PublicView{}, ErrNotFolder
		}
	}
	folder.accessRoot = &link.ID
	view.Folder = &folder
	if view.Path, err = s.crumbs(ctx, folder); err != nil {
		return PublicView{}, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+nodeColumns+` FROM drive.nodes WHERE parent_id = $1 AND trashed_at IS NULL
		ORDER BY kind = 'file', lower(name)`, folder.ID)
	if err != nil {
		return PublicView{}, fmt.Errorf("list folder: %w", err)
	}
	if view.Children, err = pgx.CollectRows(rows, scanNode); err != nil {
		return PublicView{}, fmt.Errorf("list folder: %w", err)
	}
	for i := range view.Children {
		view.Children[i].Permission = authz.View
	}
	return view, nil
}

// linkFile checks, for a running editor session, that a link still exists
// and has not expired, and that the file is still inside it and not in the
// trash. Revoking a link therefore ends sessions opened through it.
func (s *Service) linkFile(ctx context.Context, linkID, fileID uuid.UUID) (Node, error) {
	var nodeID uuid.UUID
	var expires *time.Time
	err := s.db.QueryRow(ctx, `SELECT node_id, expires_at FROM drive.links WHERE id = $1`, linkID).Scan(&nodeID, &expires)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && expires != nil && !expires.After(s.now())) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("load link: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1`, nodeID)
	if err != nil {
		return Node{}, fmt.Errorf("load node: %w", err)
	}
	link, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if err != nil {
		return Node{}, ErrNotFound
	}
	if trashed, err := s.inTrash(ctx, s.db, nodeID); err != nil || trashed {
		return Node{}, ErrNotFound
	}
	n, err := s.within(ctx, link, fileID)
	if err != nil || n.Kind != "file" || n.currentVersionID == nil {
		return Node{}, ErrNotFound
	}
	return n, nil
}

// OpenLinkFile returns the content of the linked file, or of a file inside a
// linked folder, and counts the download.
func (s *Service) OpenLinkFile(ctx context.Context, token, password string, fileID uuid.UUID) (Node, storage.Object, error) {
	link, linkID, err := s.openLink(ctx, token, password)
	if err != nil {
		return Node{}, nil, err
	}
	n, err := s.within(ctx, link, fileID)
	if err != nil {
		return Node{}, nil, err
	}
	if n.Kind != "file" || n.currentVersionID == nil {
		return Node{}, nil, ErrNotFound
	}
	obj, err := s.openVersion(ctx, *n.currentVersionID)
	if err != nil {
		return Node{}, nil, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE drive.links SET downloads = downloads + 1 WHERE id = $1`, linkID); err != nil {
		s.log.Warn("count link download", "error", err)
	}
	return n, obj, nil
}
