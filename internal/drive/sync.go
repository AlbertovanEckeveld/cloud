package drive

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Synchronisation API for the desktop app (ADR 0016). A client syncs one or
// more roots (own files, a team folder, a shared folder): it reads the whole
// tree once, then asks for changes since its cursor. Changes are state, not
// events ("this is how the node is now", or "gone from this root"), so
// delivering one twice is harmless.

const (
	syncPageSize     = 1000
	changesRetention = 30 * 24 * time.Hour
	mineRoot         = "mine"
)

// ErrCursorExpired means the client must read the whole tree again.
var ErrCursorExpired = errors.New("sync cursor expired")

// SyncRoot is a folder tree the user can synchronise.
type SyncRoot struct {
	ID         string           `json:"id"` // "mine", or the folder's ID
	Name       string           `json:"name"`
	Kind       string           `json:"kind"` // mine, team or shared
	Permission authz.Permission `json:"permission"`
}

// SyncItem is the current state of a node inside a root.
type SyncItem struct {
	ID        uuid.UUID  `json:"id"`
	ParentID  *uuid.UUID `json:"parent_id"` // nil: directly in the root
	Kind      string     `json:"kind,omitempty"`
	Name      string     `json:"name,omitempty"`
	Size      int64      `json:"size,omitempty"`
	Version   *uuid.UUID `json:"version,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Gone means the node left this root: deleted, in the trash, moved
	// elsewhere, or no longer shared. The client removes its copy.
	Gone bool `json:"gone,omitempty"`
}

// SyncPage is a batch of the tree or of changes.
type SyncPage struct {
	Cursor string     `json:"cursor"`
	Items  []SyncItem `json:"items"`
	More   bool       `json:"more"` // ask again with Cursor right away
}

// SyncRoots lists what the user can synchronise.
func (s *Service) SyncRoots(ctx context.Context, userID uuid.UUID) ([]SyncRoot, error) {
	roots := []SyncRoot{{ID: mineRoot, Name: "Mijn bestanden", Kind: "mine", Permission: authz.Manage}}
	teams, err := s.TeamFoldersFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		roots = append(roots, SyncRoot{ID: t.ID.String(), Name: t.Name, Kind: "team", Permission: t.Permission})
	}
	shared, err := s.SharedWithMe(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, sh := range shared {
		if sh.Kind == "folder" {
			roots = append(roots, SyncRoot{ID: sh.ID.String(), Name: sh.Name, Kind: "shared", Permission: sh.Permission})
		}
	}
	return roots, nil
}

// syncScope is a resolved root: the user's own top level, or a folder the
// user can see.
type syncScope struct {
	user  uuid.UUID
	owner uuid.UUID
	root  *uuid.UUID
}

func (s *Service) scope(ctx context.Context, userID uuid.UUID, root string) (syncScope, error) {
	if root == mineRoot {
		return syncScope{user: userID, owner: userID}, nil
	}
	id, err := uuid.Parse(root)
	if err != nil {
		return syncScope{}, ErrNotFound
	}
	n, err := s.authorize(ctx, s.db, userID, id, authz.View, false)
	if err != nil {
		return syncScope{}, err
	}
	if n.Kind != "folder" {
		return syncScope{}, ErrNotFolder
	}
	return syncScope{user: userID, owner: n.ownerID, root: &id}, nil
}

const syncColumns = `n.id, n.parent_id, n.kind, n.name, n.size, n.current_version_id, v.sha256, n.updated_at`

func scanSyncItem(row pgx.CollectableRow) (SyncItem, error) {
	var it SyncItem
	var sum []byte
	err := row.Scan(&it.ID, &it.ParentID, &it.Kind, &it.Name, &it.Size, &it.Version, &sum, &it.UpdatedAt)
	it.UpdatedAt = it.UpdatedAt.UTC()
	if sum != nil {
		it.SHA256 = hex.EncodeToString(sum)
	}
	return it, err
}

// position is the cursor of the current moment: every transaction below
// the snapshot's xmin has finished; later ones are read next time.
func position(ctx context.Context, q dbtx) (string, error) {
	var xmin string
	if err := q.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot())::text`).Scan(&xmin); err != nil {
		return "", fmt.Errorf("read sync position: %w", err)
	}
	return fmt.Sprintf("%s.%s.%d", xmin, uuid.Nil, time.Now().Unix()), nil
}

// SyncTree returns every live node in a root, and the cursor to ask for
// changes from. Changes made while reading may show up in the next batch
// of changes as well; that is harmless.
func (s *Service) SyncTree(ctx context.Context, userID uuid.UUID, root string) (SyncPage, error) {
	sc, err := s.scope(ctx, userID, root)
	if err != nil {
		return SyncPage{}, err
	}
	var page SyncPage
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if page.Cursor, err = position(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE tree AS (
				SELECT id FROM drive.nodes
				WHERE trashed_at IS NULL AND (($2::uuid IS NULL AND owner_id = $1 AND parent_id IS NULL) OR parent_id = $2)
				UNION ALL
				SELECT c.id FROM drive.nodes c JOIN tree t ON c.parent_id = t.id WHERE c.trashed_at IS NULL)
			SELECT `+syncColumns+` FROM tree JOIN drive.nodes n ON n.id = tree.id
			LEFT JOIN drive.versions v ON v.id = n.current_version_id`, sc.owner, sc.root)
		if err != nil {
			return fmt.Errorf("read tree: %w", err)
		}
		page.Items, err = pgx.CollectRows(rows, scanSyncItem)
		return err
	})
	if err != nil {
		return SyncPage{}, err
	}
	for i := range page.Items {
		if sc.root != nil && page.Items[i].ParentID != nil && *page.Items[i].ParentID == *sc.root {
			page.Items[i].ParentID = nil
		}
	}
	return page, nil
}

// SyncChanges returns what changed in a root since the cursor.
func (s *Service) SyncChanges(ctx context.Context, userID uuid.UUID, root, cursor string) (SyncPage, error) {
	parts := strings.Split(cursor, ".")
	if len(parts) != 3 {
		return SyncPage{}, &InputError{"Ongeldige cursor."}
	}
	txid, lastID := parts[0], parts[1]
	if _, err := uuid.Parse(lastID); err != nil {
		return SyncPage{}, &InputError{"Ongeldige cursor."}
	}
	var issued int64
	if _, err := fmt.Sscan(parts[2], &issued); err != nil || s.now().Sub(time.Unix(issued, 0)) > changesRetention-time.Hour {
		return SyncPage{}, ErrCursorExpired // older changes may have been pruned
	}
	sc, err := s.scope(ctx, userID, root)
	if err != nil {
		return SyncPage{}, err
	}

	rows, err := s.db.Query(ctx, `
		SELECT txid::text, id, node_id FROM drive.changes
		WHERE owner_id = $4 AND (txid, id) > ($1::xid8, $2::uuid) AND txid < pg_snapshot_xmin(pg_current_snapshot())
		ORDER BY txid, id LIMIT $3`, txid, lastID, syncPageSize+1, sc.owner)
	if err != nil {
		return SyncPage{}, &InputError{"Ongeldige cursor."}
	}
	type change struct { // exported fields: pgx fills them by reflection
		Txid string
		ID   uuid.UUID
		Node uuid.UUID
	}
	changes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[change])
	if err != nil {
		return SyncPage{}, fmt.Errorf("read changes: %w", err)
	}
	page := SyncPage{Items: []SyncItem{}}
	if len(changes) > syncPageSize {
		changes, page.More = changes[:syncPageSize], true
	}
	if len(changes) == 0 {
		// Nothing new: move the cursor up to now, so it does not age.
		if page.Cursor, err = position(ctx, s.db); err != nil {
			return SyncPage{}, err
		}
		// A long transaction can hold xmin below the cursor: never move back.
		now, _ := strconv.ParseUint(strings.Split(page.Cursor, ".")[0], 10, 64)
		if prev, _ := strconv.ParseUint(txid, 10, 64); now <= prev {
			page.Cursor = fmt.Sprintf("%s.%s.%d", txid, lastID, s.now().Unix())
		}
		return page, nil
	}
	last := changes[len(changes)-1]
	page.Cursor = fmt.Sprintf("%s.%s.%d", last.Txid, last.ID, s.now().Unix())

	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, c := range changes {
		if !seen[c.Node] {
			seen[c.Node] = true
			ids = append(ids, c.Node)
		}
	}
	if page.Items, err = s.syncStates(ctx, sc, ids); err != nil {
		return SyncPage{}, err
	}
	return page, nil
}

// syncStates returns the current state of nodes as seen from a root.
func (s *Service) syncStates(ctx context.Context, sc syncScope, ids []uuid.UUID) ([]SyncItem, error) {
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE chain AS (
			SELECT n.id AS node, n.id, n.parent_id, n.owner_id, n.trashed_at, 0 AS depth FROM drive.nodes n WHERE n.id = ANY($1)
			UNION ALL
			SELECT c.node, p.id, p.parent_id, p.owner_id, p.trashed_at, c.depth + 1 FROM chain c JOIN drive.nodes p ON p.id = c.parent_id)
		SELECT node, array_agg(id ORDER BY depth), bool_or(trashed_at IS NOT NULL), min(owner_id::text)
		FROM chain GROUP BY node`, ids)
	if err != nil {
		return nil, fmt.Errorf("read paths: %w", err)
	}
	type lineage struct {
		path    []uuid.UUID
		trashed bool
		owner   string
	}
	paths := map[uuid.UUID]lineage{}
	for rows.Next() {
		var id uuid.UUID
		var l lineage
		if err := rows.Scan(&id, &l.path, &l.trashed, &l.owner); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read paths: %w", err)
		}
		paths[id] = l
	}
	rows.Close()

	inScope := func(l lineage) bool {
		if l.trashed || l.owner != sc.owner.String() {
			return false
		}
		if sc.root == nil {
			return true
		}
		// Inside the root folder, not the root itself.
		for _, id := range l.path[1:] {
			if id == *sc.root {
				return true
			}
		}
		return false
	}
	var live []uuid.UUID
	items := make([]SyncItem, 0, len(ids))
	for _, id := range ids {
		if l, ok := paths[id]; ok && inScope(l) {
			live = append(live, id)
		} else {
			items = append(items, SyncItem{ID: id, Gone: true, UpdatedAt: s.now().UTC()})
		}
	}
	if len(live) > 0 {
		rows, err := s.db.Query(ctx, `SELECT `+syncColumns+` FROM drive.nodes n
			LEFT JOIN drive.versions v ON v.id = n.current_version_id WHERE n.id = ANY($1)`, live)
		if err != nil {
			return nil, fmt.Errorf("read nodes: %w", err)
		}
		found, err := pgx.CollectRows(rows, scanSyncItem)
		if err != nil {
			return nil, fmt.Errorf("read nodes: %w", err)
		}
		for _, it := range found {
			if sc.root != nil && it.ParentID != nil && *it.ParentID == *sc.root {
				it.ParentID = nil
			}
			items = append(items, it)
		}
	}
	return items, nil
}

// pruneChanges forgets changes older than the retention period; clients
// with an older cursor read the whole tree again.
func (s *Service) pruneChanges(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM drive.changes WHERE at < $1`, s.now().Add(-changesRetention)); err != nil {
		return fmt.Errorf("prune changes: %w", err)
	}
	return nil
}

func (s *Service) handleSyncRoots(w http.ResponseWriter, r *http.Request) {
	roots, err := s.SyncRoots(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Roots []SyncRoot `json:"roots"`
	}{roots})
}

func (s *Service) handleSyncTree(w http.ResponseWriter, r *http.Request) {
	page, err := s.SyncTree(r.Context(), s.userID(r), r.URL.Query().Get("root"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, page)
}

func (s *Service) handleSyncChanges(w http.ResponseWriter, r *http.Request) {
	page, err := s.SyncChanges(r.Context(), s.userID(r), r.URL.Query().Get("root"), r.URL.Query().Get("cursor"))
	if errors.Is(err, ErrCursorExpired) {
		httpserver.WriteError(w, http.StatusGone, "cursor_expired", "Lees de hele map opnieuw in.")
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, page)
}
