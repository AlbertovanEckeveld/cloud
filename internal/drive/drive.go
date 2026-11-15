// Package drive stores the users' files and folders: the tree and versions
// in Postgres, the content in S3 (one immutable object per version).
package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"

	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

// Limits and retention.
const (
	DefaultMaxFileSize int64 = 10 << 30 // 10 GiB
	trashRetention           = 30 * 24 * time.Hour
	maxNameBytes             = 255
)

// Errors returned to the HTTP layer.
var (
	ErrNotFound      = errors.New("not found")
	ErrNameTaken     = errors.New("name already used in this folder")
	ErrNotFolder     = errors.New("not a folder")
	ErrCycle         = errors.New("cannot move a folder into itself")
	ErrQuotaExceeded = errors.New("storage quota exceeded")
	ErrTooLarge      = errors.New("file too large")
	ErrIncomplete    = errors.New("upload incomplete")
)

// InputError is a validation error whose message is safe to show to users.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

// Objects is the part of the S3 client drive uses.
type Objects interface {
	PutStream(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (storage.Object, int64, error)
	Delete(ctx context.Context, key string) error
}

// Options configures the service.
type Options struct {
	Logger      *slog.Logger
	MaxFileSize int64
}

// Service implements the drive module.
type Service struct {
	db      *pgxpool.Pool
	objects Objects
	log     *slog.Logger
	maxFile int64
	now     func() time.Time
	// userID identifies the signed-in user; set by Routes.
	userID func(*http.Request) uuid.UUID
}

// New returns the drive service.
func New(db *pgxpool.Pool, objects Objects, opts Options) *Service {
	maxFile := opts.MaxFileSize
	if maxFile <= 0 {
		maxFile = DefaultMaxFileSize
	}
	return &Service{db: db, objects: objects, log: opts.Logger, maxFile: maxFile, now: time.Now}
}

// Node is a file or folder.
type Node struct {
	ID          uuid.UUID  `json:"id"`
	ParentID    *uuid.UUID `json:"parent_id"`
	Kind        string     `json:"kind"` // "folder" or "file"
	Name        string     `json:"name"`
	Size        int64      `json:"size"`
	ContentType string     `json:"content_type"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	TrashedAt   *time.Time `json:"trashed_at,omitempty"`

	ownerID          uuid.UUID
	currentVersionID *uuid.UUID
}

const nodeColumns = `id, parent_id, kind, name, size, content_type, created_at, updated_at, trashed_at, owner_id, current_version_id`

func scanNode(row pgx.CollectableRow) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Name, &n.Size, &n.ContentType, &n.CreatedAt, &n.UpdatedAt,
		&n.TrashedAt, &n.ownerID, &n.currentVersionID)
	n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
	if n.TrashedAt != nil {
		*n.TrashedAt = n.TrashedAt.UTC()
	}
	return n, err
}

// dbtx is implemented by both the pool and a transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// normalizeName validates a file or folder name and returns it in Unicode
// NFC form: macOS sends decomposed characters (NFD), other systems composed
// ones; without normalising, "é" could exist twice in one folder.
func normalizeName(name string) (string, error) {
	name = norm.NFC.String(strings.TrimSpace(name))
	switch {
	case name == "" || name == "." || name == "..":
		return "", &InputError{"Geef een naam op."}
	case len(name) > maxNameBytes || !utf8.ValidString(name):
		return "", &InputError{"De naam is te lang."}
	case strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl):
		return "", &InputError{`Een naam mag geen / of \ of onzichtbare tekens bevatten.`}
	}
	return name, nil
}

func uniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

// authorize loads a node for a user and checks access. It is the single
// place where drive decides who may see or change what: for now only the
// owner; sharing (phase 2.2) extends this function. Nodes the user may not
// see are reported as not found, so their existence does not leak.
// Trashed nodes (or nodes inside a trashed folder) are only found when
// trashed is true.
func (s *Service) authorize(ctx context.Context, q dbtx, userID, nodeID uuid.UUID, trashed bool) (Node, error) {
	rows, err := q.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1 AND owner_id = $2`, nodeID, userID)
	if err != nil {
		return Node{}, fmt.Errorf("load node: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("load node: %w", err)
	}
	inTrash, err := s.inTrash(ctx, q, nodeID)
	if err != nil {
		return Node{}, err
	}
	if inTrash != trashed {
		return Node{}, ErrNotFound
	}
	return n, nil
}

// inTrash reports whether a node or any of its ancestors is trashed.
func (s *Service) inTrash(ctx context.Context, q dbtx, nodeID uuid.UUID) (bool, error) {
	var trashed bool
	err := q.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id, trashed_at FROM drive.nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, n.trashed_at FROM drive.nodes n JOIN chain c ON n.id = c.parent_id)
		SELECT COALESCE(bool_or(trashed_at IS NOT NULL), false) FROM chain`, nodeID).Scan(&trashed)
	if err != nil {
		return false, fmt.Errorf("check trash: %w", err)
	}
	return trashed, nil
}

// folder checks that parentID (nil: the top level) is a live folder of userID.
func (s *Service) folder(ctx context.Context, q dbtx, userID uuid.UUID, parentID *uuid.UUID) error {
	if parentID == nil {
		return nil
	}
	n, err := s.authorize(ctx, q, userID, *parentID, false)
	if err != nil {
		return err
	}
	if n.Kind != "folder" {
		return ErrNotFolder
	}
	return nil
}

// Crumb is one step of a folder path.
type Crumb struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// FolderView is a folder with its path and contents.
type FolderView struct {
	Folder   *Node   `json:"folder"` // nil for the top level
	Path     []Crumb `json:"path"`
	Children []Node  `json:"children"`
}

// List returns a folder's contents (folderID nil: the top level), folders first.
func (s *Service) List(ctx context.Context, userID uuid.UUID, folderID *uuid.UUID) (FolderView, error) {
	view := FolderView{Path: []Crumb{}}
	if folderID != nil {
		n, err := s.authorize(ctx, s.db, userID, *folderID, false)
		if err != nil {
			return FolderView{}, err
		}
		if n.Kind != "folder" {
			return FolderView{}, ErrNotFolder
		}
		view.Folder = &n
		rows, err := s.db.Query(ctx, `
			WITH RECURSIVE chain AS (
				SELECT id, parent_id, name, 0 AS depth FROM drive.nodes WHERE id = $1
				UNION ALL
				SELECT n.id, n.parent_id, n.name, c.depth + 1 FROM drive.nodes n JOIN chain c ON n.id = c.parent_id)
			SELECT id, name FROM chain ORDER BY depth DESC`, *folderID)
		if err != nil {
			return FolderView{}, fmt.Errorf("load path: %w", err)
		}
		if view.Path, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Crumb]); err != nil {
			return FolderView{}, fmt.Errorf("load path: %w", err)
		}
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+nodeColumns+` FROM drive.nodes
		WHERE owner_id = $1 AND parent_id IS NOT DISTINCT FROM $2 AND trashed_at IS NULL
		ORDER BY kind = 'file', lower(name)`, userID, folderID)
	if err != nil {
		return FolderView{}, fmt.Errorf("list folder: %w", err)
	}
	if view.Children, err = pgx.CollectRows(rows, scanNode); err != nil {
		return FolderView{}, fmt.Errorf("list folder: %w", err)
	}
	return view, nil
}

// CreateFolder makes a folder in parentID (nil: the top level).
func (s *Service) CreateFolder(ctx context.Context, userID uuid.UUID, parentID *uuid.UUID, name string) (Node, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Node{}, err
	}
	if err := s.folder(ctx, s.db, userID, parentID); err != nil {
		return Node{}, err
	}
	now := s.now().UTC()
	n := Node{ID: newID(), ParentID: parentID, Kind: "folder", Name: name, CreatedAt: now, UpdatedAt: now, ownerID: userID}
	_, err = s.db.Exec(ctx, `
		INSERT INTO drive.nodes (id, owner_id, parent_id, kind, name, created_at, updated_at)
		VALUES ($1, $2, $3, 'folder', $4, $5, $5)`, n.ID, userID, parentID, name, now)
	if uniqueViolation(err) {
		return Node{}, ErrNameTaken
	}
	if err != nil {
		return Node{}, fmt.Errorf("create folder: %w", err)
	}
	return n, nil
}

// Change describes a rename and/or move; nil fields stay unchanged. For a
// move, Parent is the new folder, or nil with ToTop set for the top level.
type Change struct {
	Name   *string
	Parent *uuid.UUID
	ToTop  bool
}

// Update renames or moves a node.
func (s *Service) Update(ctx context.Context, userID, nodeID uuid.UUID, ch Change) (Node, error) {
	var out Node
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		n, err := s.authorize(ctx, tx, userID, nodeID, false)
		if err != nil {
			return err
		}
		if ch.Name != nil {
			if n.Name, err = normalizeName(*ch.Name); err != nil {
				return err
			}
		}
		if ch.Parent != nil || ch.ToTop {
			if ch.ToTop {
				n.ParentID = nil
			} else {
				if err := s.folder(ctx, tx, userID, ch.Parent); err != nil {
					return err
				}
				// A folder cannot move into itself or one of its descendants.
				var cycle bool
				if err := tx.QueryRow(ctx, `
					WITH RECURSIVE chain AS (
						SELECT id, parent_id FROM drive.nodes WHERE id = $1
						UNION ALL
						SELECT n.id, n.parent_id FROM drive.nodes n JOIN chain c ON n.id = c.parent_id)
					SELECT EXISTS (SELECT 1 FROM chain WHERE id = $2)`, *ch.Parent, nodeID).Scan(&cycle); err != nil {
					return fmt.Errorf("check move: %w", err)
				}
				if cycle {
					return ErrCycle
				}
				n.ParentID = ch.Parent
			}
		}
		n.UpdatedAt = s.now().UTC()
		_, err = tx.Exec(ctx, `UPDATE drive.nodes SET name = $2, parent_id = $3, updated_at = $4 WHERE id = $1`,
			nodeID, n.Name, n.ParentID, n.UpdatedAt)
		if uniqueViolation(err) {
			return ErrNameTaken
		}
		if err != nil {
			return fmt.Errorf("update node: %w", err)
		}
		out = n
		return nil
	})
	return out, err
}

// Run purges expired trash and deletes unreferenced objects until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	lastPurge := time.Time{}
	for {
		if s.now().Sub(lastPurge) >= time.Hour {
			if err := s.purgeExpiredTrash(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("purge expired trash", "error", err)
			}
			lastPurge = s.now()
		}
		if err := s.deleteObjects(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("delete unreferenced objects", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
