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
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
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
	ErrForbidden     = errors.New("insufficient permission")
)

// nodeType is the resource type of files and folders in the sharing model.
const nodeType = "drive.node"

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
	// Sharing is the central authorization of the identity module.
	Sharing authz.Authorizer
	// Office enables editing documents with Collabora Online; nil disables it.
	Office *Office
}

// Service implements the drive module.
type Service struct {
	db      *pgxpool.Pool
	objects Objects
	sharing authz.Authorizer
	log     *slog.Logger
	maxFile int64
	now     func() time.Time
	locks   davLocks
	office  *office
	// userID identifies the signed-in user and audit records administrative
	// actions; both set by Routes.
	userID func(*http.Request) uuid.UUID
	audit  func(r *http.Request, event string, details map[string]any)
}

// New returns the drive service.
func New(db *pgxpool.Pool, objects Objects, opts Options) *Service {
	maxFile := opts.MaxFileSize
	if maxFile <= 0 {
		maxFile = DefaultMaxFileSize
	}
	s := &Service{db: db, objects: objects, sharing: opts.Sharing, log: opts.Logger, maxFile: maxFile, now: time.Now}
	if opts.Office != nil {
		s.office = &office{cfg: *opts.Office}
	}
	return s
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
	// UpdatedBy is who last changed the node; UpdatedByName is filled in
	// for listings.
	UpdatedBy     *uuid.UUID `json:"updated_by"`
	UpdatedByName string     `json:"updated_by_name,omitempty"`
	TrashedAt     *time.Time `json:"trashed_at,omitempty"`
	// Permission is what the requesting user may do with the node.
	Permission authz.Permission `json:"permission"`
	// Shared is set when the node itself is shared (shown to managers only).
	Shared bool `json:"shared,omitempty"`

	ownerID          uuid.UUID
	currentVersionID *uuid.UUID
	// accessRoot is the highest node in the path the user has access to:
	// the top level for the owner, the shared node for anyone else.
	accessRoot *uuid.UUID
}

const nodeColumns = `id, parent_id, kind, name, size, content_type, created_at, updated_at, trashed_at, owner_id, current_version_id, updated_by`

// prefixed returns nodeColumns qualified with a table alias, for joins.
func prefixed(alias string) string {
	cols := strings.Split(nodeColumns, ", ")
	for i := range cols {
		cols[i] = alias + "." + cols[i]
	}
	return strings.Join(cols, ", ")
}

func scanNode(row pgx.CollectableRow) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Name, &n.Size, &n.ContentType, &n.CreatedAt, &n.UpdatedAt,
		&n.TrashedAt, &n.ownerID, &n.currentVersionID, &n.UpdatedBy)
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

// authorize loads a node and checks that the user holds at least need on
// it. It is the single place where drive decides who may see or change what:
// the owner may do everything; anyone else needs a share on the node or on a
// folder above it (shares are inherited down the tree), resolved by the
// identity module. Nodes the user cannot see are reported as not found, so
// their existence does not leak; too weak a permission is ErrForbidden.
// Trashed nodes (or nodes inside a trashed folder) are only found when
// trashed is true, and only by their owner.
func (s *Service) authorize(ctx context.Context, q dbtx, userID, nodeID uuid.UUID, need authz.Permission, trashed bool) (Node, error) {
	rows, err := q.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1`, nodeID)
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
	path, inTrash, err := s.lineage(ctx, q, nodeID)
	if err != nil {
		return Node{}, err
	}
	if inTrash != trashed {
		return Node{}, ErrNotFound
	}
	if n.ownerID == userID {
		n.Permission = authz.Manage
		return n, nil
	}
	if trashed {
		return Node{}, ErrNotFound // the trash belongs to the owner
	}
	perms, err := s.sharing.Permissions(ctx, userID, nodeType, path)
	if err != nil {
		return Node{}, fmt.Errorf("check shares: %w", err)
	}
	for _, id := range path { // nearest first, so the last match is the highest
		if p, ok := perms[id]; ok {
			n.Permission = authz.Max(n.Permission, p)
			n.accessRoot = &id
		}
	}
	if n.Permission == authz.None {
		return Node{}, ErrNotFound
	}
	if !n.Permission.Allows(need) {
		return Node{}, ErrForbidden
	}
	if *n.accessRoot == n.ID {
		n.ParentID = nil // the owner's folders above a share stay private
	}
	return n, nil
}

// lineage returns a node and its ancestors, nearest first, and whether any
// of them is in the trash.
func (s *Service) lineage(ctx context.Context, q dbtx, nodeID uuid.UUID) ([]uuid.UUID, bool, error) {
	var ids []uuid.UUID
	var trashed bool
	err := q.QueryRow(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id, trashed_at, 0 AS depth FROM drive.nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, n.trashed_at, c.depth + 1 FROM drive.nodes n JOIN chain c ON n.id = c.parent_id)
		SELECT array_agg(id ORDER BY depth), COALESCE(bool_or(trashed_at IS NOT NULL), false) FROM chain`, nodeID).Scan(&ids, &trashed)
	if err != nil {
		return nil, false, fmt.Errorf("load path: %w", err)
	}
	return ids, trashed, nil
}

// inTrash reports whether a node or any of its ancestors is trashed.
func (s *Service) inTrash(ctx context.Context, q dbtx, nodeID uuid.UUID) (bool, error) {
	_, trashed, err := s.lineage(ctx, q, nodeID)
	return trashed, err
}

// target checks that the user may add to a folder (nil: their own top
// level) and returns the folder's owner and the user's permission there.
// Everything in a folder belongs to the folder's owner, also when someone it
// is shared with uploads it: a tree never has more than one owner.
func (s *Service) target(ctx context.Context, q dbtx, userID uuid.UUID, parentID *uuid.UUID) (uuid.UUID, authz.Permission, error) {
	if parentID == nil {
		return userID, authz.Manage, nil
	}
	n, err := s.authorize(ctx, q, userID, *parentID, authz.Edit, false)
	if err != nil {
		return uuid.Nil, authz.None, err
	}
	if n.Kind != "folder" {
		return uuid.Nil, authz.None, ErrNotFolder
	}
	return n.ownerID, n.Permission, nil
}

// Crumb is one step of a folder path.
type Crumb struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// FolderView is a folder with its path and contents.
type FolderView struct {
	Folder *Node `json:"folder"` // nil for the top level
	// Path runs from the highest folder the user has access to down to
	// Folder: from the top level for the owner, from the share otherwise.
	Path     []Crumb `json:"path"`
	Children []Node  `json:"children"`
	// Shared is set when the folder is someone else's, shared with the user.
	Shared bool `json:"shared"`
	// Team is set when the folder belongs to a team folder.
	Team *TeamRef `json:"team"`
}

// List returns a folder's contents (folderID nil: the top level), folders first.
func (s *Service) List(ctx context.Context, userID uuid.UUID, folderID *uuid.UUID) (FolderView, error) {
	view := FolderView{Path: []Crumb{}}
	var rows pgx.Rows
	var err error
	if folderID == nil {
		rows, err = s.db.Query(ctx, `
			SELECT `+nodeColumns+` FROM drive.nodes WHERE owner_id = $1 AND parent_id IS NULL AND trashed_at IS NULL
			ORDER BY kind = 'file', lower(name)`, userID)
	} else {
		n, err := s.authorize(ctx, s.db, userID, *folderID, authz.View, false)
		if err != nil {
			return FolderView{}, err
		}
		if n.Kind != "folder" {
			return FolderView{}, ErrNotFolder
		}
		view.Folder, view.Shared = &n, n.ownerID != userID
		if view.Path, err = s.crumbs(ctx, n); err != nil {
			return FolderView{}, err
		}
		if view.Shared {
			if view.Team, err = s.teamOf(ctx, n.ownerID); err != nil {
				return FolderView{}, err
			}
		}
		rows, err = s.db.Query(ctx, `
			SELECT `+nodeColumns+` FROM drive.nodes WHERE parent_id = $1 AND trashed_at IS NULL
			ORDER BY kind = 'file', lower(name)`, *folderID)
	}
	if err != nil {
		return FolderView{}, fmt.Errorf("list folder: %w", err)
	}
	if view.Children, err = pgx.CollectRows(rows, scanNode); err != nil {
		return FolderView{}, fmt.Errorf("list folder: %w", err)
	}
	inherited := authz.Manage // the user's own top level
	if view.Folder != nil {
		inherited = view.Folder.Permission
	}
	if err := s.annotate(ctx, userID, inherited, view.Children); err != nil {
		return FolderView{}, err
	}
	if err := s.nameEditors(ctx, view.Children); err != nil {
		return FolderView{}, err
	}
	return view, nil
}

// crumbs returns the path to a folder the user may see, starting at their
// access root, so the names of the owner's folders above a share stay private.
func (s *Service) crumbs(ctx context.Context, n Node) ([]Crumb, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id, parent_id, name, 0 AS depth FROM drive.nodes WHERE id = $1
			UNION ALL
			SELECT n.id, n.parent_id, n.name, c.depth + 1 FROM drive.nodes n JOIN chain c ON n.id = c.parent_id
			WHERE c.id IS DISTINCT FROM $2)
		SELECT id, name FROM chain ORDER BY depth DESC`, n.ID, n.accessRoot)
	if err != nil {
		return nil, fmt.Errorf("load path: %w", err)
	}
	path, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Crumb])
	if err != nil {
		return nil, fmt.Errorf("load path: %w", err)
	}
	return path, nil
}

// annotate sets the user's permission on nodes in a folder where they hold
// inherited, plus anything shared with them on the nodes themselves, and
// marks which nodes are shared for users who may manage them.
func (s *Service) annotate(ctx context.Context, userID uuid.UUID, inherited authz.Permission, nodes []Node) error {
	if len(nodes) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	own := map[uuid.UUID]authz.Permission{}
	if inherited != authz.Manage {
		var err error
		if own, err = s.sharing.Permissions(ctx, userID, nodeType, ids); err != nil {
			return fmt.Errorf("check shares: %w", err)
		}
	}
	shared, err := s.sharing.Shared(ctx, nodeType, ids)
	if err != nil {
		return fmt.Errorf("check shares: %w", err)
	}
	for i := range nodes {
		nodes[i].Permission = authz.Max(inherited, own[nodes[i].ID])
		nodes[i].Shared = shared[nodes[i].ID] && nodes[i].Permission == authz.Manage
	}
	return nil
}

// nameEditors fills in who last changed each node.
func (s *Service) nameEditors(ctx context.Context, nodes []Node) error {
	var ids []uuid.UUID
	for _, n := range nodes {
		if n.UpdatedBy != nil {
			ids = append(ids, *n.UpdatedBy)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names, err := s.sharing.DisplayNames(ctx, ids)
	if err != nil {
		return fmt.Errorf("load editor names: %w", err)
	}
	for i := range nodes {
		if nodes[i].UpdatedBy != nil {
			nodes[i].UpdatedByName = names[*nodes[i].UpdatedBy]
		}
	}
	return nil
}

// CreateFolder makes a folder in parentID (nil: the top level).
func (s *Service) CreateFolder(ctx context.Context, userID uuid.UUID, parentID *uuid.UUID, name string) (Node, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Node{}, err
	}
	owner, perm, err := s.target(ctx, s.db, userID, parentID)
	if err != nil {
		return Node{}, err
	}
	now := s.now().UTC()
	n := Node{ID: newID(), ParentID: parentID, Kind: "folder", Name: name, CreatedAt: now, UpdatedAt: now,
		UpdatedBy: &userID, Permission: perm, ownerID: owner}
	_, err = s.db.Exec(ctx, `
		INSERT INTO drive.nodes (id, owner_id, parent_id, kind, name, created_at, updated_at, updated_by)
		VALUES ($1, $2, $3, 'folder', $4, $5, $5, $6)`, n.ID, owner, parentID, name, now, userID)
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

// Update renames or moves a node. A node can only move within its owner's
// tree, into a folder where the user may edit: files never change owner.
func (s *Service) Update(ctx context.Context, userID, nodeID uuid.UUID, ch Change) (Node, error) {
	var out Node
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		n, err := s.authorize(ctx, tx, userID, nodeID, authz.Edit, false)
		if err != nil {
			return err
		}
		if root, err := s.isTeamRoot(ctx, tx, nodeID); err != nil {
			return err
		} else if root {
			return errTeamRoot
		}
		parentID := n.ParentID
		if n.accessRoot != nil && *n.accessRoot == n.ID {
			parentID = nil // hidden from the user, but it stays where it is
			if err := tx.QueryRow(ctx, `SELECT parent_id FROM drive.nodes WHERE id = $1`, nodeID).Scan(&parentID); err != nil {
				return fmt.Errorf("load node: %w", err)
			}
		}
		if ch.Name != nil {
			if n.Name, err = normalizeName(*ch.Name); err != nil {
				return err
			}
		}
		if ch.Parent != nil || ch.ToTop {
			owner, _, err := s.target(ctx, tx, userID, ch.Parent)
			if err != nil {
				return err
			}
			if owner != n.ownerID {
				return &InputError{"Je kunt dit alleen verplaatsen binnen de mappen van dezelfde eigenaar of teammap."}
			}
			if ch.Parent != nil {
				// A folder cannot move into itself or one of its descendants.
				path, _, err := s.lineage(ctx, tx, *ch.Parent)
				if err != nil {
					return err
				}
				if slices.Contains(path, nodeID) {
					return ErrCycle
				}
			}
			parentID, n.ParentID = ch.Parent, ch.Parent
		}
		n.UpdatedAt, n.UpdatedBy = s.now().UTC(), &userID
		_, err = tx.Exec(ctx, `UPDATE drive.nodes SET name = $2, parent_id = $3, updated_at = $4, updated_by = $5 WHERE id = $1`,
			nodeID, n.Name, parentID, n.UpdatedAt, userID)
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
			if err := s.pruneChanges(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("prune sync changes", "error", err)
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
