package drive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// TeamFolder is a folder an administrator created for groups (ADR 0011).
// The team folder owns everything in it; groups get access through shares on
// its root folder. Administrators manage it but cannot see inside unless they
// are in one of its groups.
type TeamFolder struct {
	ID        uuid.UUID     `json:"id"`
	RootID    uuid.UUID     `json:"root_id"`
	Name      string        `json:"name"`
	Groups    []authz.Grant `json:"groups"`
	Usage     Usage         `json:"usage"`
	CreatedAt time.Time     `json:"created_at"`
	// DeletedAt is set while the team folder is in the trash ("opgeheven").
	DeletedAt *time.Time `json:"deleted_at"`
}

var errTeamRoot = &InputError{"Een teammap kan alleen een beheerder hernoemen, opheffen of delen."}

// teamRootOf returns the root folder of a team folder, or ErrNotFound when
// ownerID is not a team folder (but a user).
func (s *Service) teamRootOf(ctx context.Context, q dbtx, ownerID uuid.UUID) (uuid.UUID, error) {
	var root uuid.UUID
	err := q.QueryRow(ctx, `SELECT root_id FROM drive.team_folders WHERE id = $1`, ownerID).Scan(&root)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("load team folder: %w", err)
	}
	return root, nil
}

// isTeamRoot reports whether a node is the root folder of a team folder.
func (s *Service) isTeamRoot(ctx context.Context, q dbtx, nodeID uuid.UUID) (bool, error) {
	var root bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM drive.team_folders WHERE root_id = $1)`, nodeID).Scan(&root); err != nil {
		return false, fmt.Errorf("check team folder: %w", err)
	}
	return root, nil
}

// TeamRef identifies the team folder a folder belongs to.
type TeamRef struct {
	ID     uuid.UUID `json:"id"`
	RootID uuid.UUID `json:"root_id"`
	Name   string    `json:"name"`
}

// teamOf returns the team folder that owns ownerID, or nil for a user.
func (s *Service) teamOf(ctx context.Context, ownerID uuid.UUID) (*TeamRef, error) {
	var t TeamRef
	err := s.db.QueryRow(ctx, `
		SELECT t.id, t.root_id, r.name FROM drive.team_folders t JOIN drive.nodes r ON r.id = t.root_id
		WHERE t.id = $1`, ownerID).Scan(&t.ID, &t.RootID, &t.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load team folder: %w", err)
	}
	return &t, nil
}

// TeamFoldersFor lists the root folders of the team folders a user can open.
func (s *Service) TeamFoldersFor(ctx context.Context, userID uuid.UUID) ([]Node, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+prefixed("n")+` FROM drive.team_folders t JOIN drive.nodes n ON n.id = t.root_id
		WHERE n.trashed_at IS NULL ORDER BY lower(n.name)`)
	if err != nil {
		return nil, fmt.Errorf("list team folders: %w", err)
	}
	roots, err := pgx.CollectRows(rows, scanNode)
	if err != nil || len(roots) == 0 {
		return []Node{}, err
	}
	ids := make([]uuid.UUID, len(roots))
	for i, n := range roots {
		ids[i] = n.ID
	}
	perms, err := s.sharing.Permissions(ctx, userID, nodeType, ids)
	if err != nil {
		return nil, fmt.Errorf("check shares: %w", err)
	}
	out := []Node{}
	for _, n := range roots {
		if p, ok := perms[n.ID]; ok {
			n.Permission = p
			out = append(out, n)
		}
	}
	return out, nil
}

// ListTeamTrash lists what was deleted inside a team folder; members who may
// edit it may see and restore it.
func (s *Service) ListTeamTrash(ctx context.Context, userID, teamID uuid.UUID) (TeamRef, []Node, error) {
	root, err := s.teamRootOf(ctx, s.db, teamID)
	if err != nil {
		return TeamRef{}, nil, err
	}
	r, err := s.authorize(ctx, s.db, userID, root, authz.Edit, false)
	if err != nil {
		return TeamRef{}, nil, err
	}
	team := TeamRef{ID: teamID, RootID: root, Name: r.Name}
	rows, err := s.db.Query(ctx, `
		SELECT `+nodeColumns+` FROM drive.nodes
		WHERE owner_id = $1 AND trashed_at IS NOT NULL AND id <> $2 ORDER BY trashed_at DESC`, teamID, root)
	if err != nil {
		return TeamRef{}, nil, fmt.Errorf("list trash: %w", err)
	}
	nodes, err := pgx.CollectRows(rows, scanNode)
	for i := range nodes {
		nodes[i].Permission = r.Permission
	}
	return team, nodes, err
}

// EmptyTeamTrash removes everything in a team folder's trash for good; it
// needs manage on the team folder.
func (s *Service) EmptyTeamTrash(ctx context.Context, userID, teamID uuid.UUID) error {
	var tree []uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		root, err := s.teamRootOf(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if _, err := s.authorize(ctx, tx, userID, root, authz.Manage, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM drive.nodes WHERE owner_id = $1 AND trashed_at IS NOT NULL AND id <> $2`, teamID, root)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		tree, err = s.purge(ctx, tx, ids)
		return err
	})
	if err == nil {
		s.revokeShares(ctx, tree)
		s.deleteObjectsSoon(ctx)
	}
	return err
}

// ListTeamFolders returns every team folder with its groups and usage, for
// administrators. It shows no contents.
func (s *Service) ListTeamFolders(ctx context.Context) ([]TeamFolder, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.root_id, r.name, t.created_at, r.trashed_at
		FROM drive.team_folders t JOIN drive.nodes r ON r.id = t.root_id ORDER BY lower(r.name)`)
	if err != nil {
		return nil, fmt.Errorf("list team folders: %w", err)
	}
	folders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TeamFolder, error) {
		var t TeamFolder
		err := row.Scan(&t.ID, &t.RootID, &t.Name, &t.CreatedAt, &t.DeletedAt)
		t.CreatedAt = t.CreatedAt.UTC()
		if t.DeletedAt != nil {
			*t.DeletedAt = t.DeletedAt.UTC()
		}
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("list team folders: %w", err)
	}
	for i := range folders {
		if folders[i].Groups, err = s.sharing.Grants(ctx, nodeType, folders[i].RootID); err != nil {
			return nil, fmt.Errorf("list team folder groups: %w", err)
		}
		if folders[i].Usage, err = s.Usage(ctx, folders[i].ID); err != nil {
			return nil, err
		}
	}
	return folders, nil
}

// TeamFolder returns one team folder, for administrators.
func (s *Service) TeamFolder(ctx context.Context, id uuid.UUID) (TeamFolder, error) {
	all, err := s.ListTeamFolders(ctx)
	if err != nil {
		return TeamFolder{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return TeamFolder{}, ErrNotFound
}

// lockTeamFolders serialises creating and renaming team folders, so two
// administrators cannot create the same name at once.
func lockTeamFolders(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `LOCK TABLE drive.team_folders IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock team folders: %w", err)
	}
	return nil
}

// teamNameTaken reports whether another live team folder has this name.
func teamNameTaken(ctx context.Context, tx pgx.Tx, name string, except uuid.UUID) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM drive.team_folders t JOIN drive.nodes r ON r.id = t.root_id
		               WHERE lower(r.name) = lower($1) AND r.trashed_at IS NULL AND t.id <> $2)`, name, except).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("check team folder name: %w", err)
	}
	return taken, nil
}

// CreateTeamFolder makes an empty team folder. Nobody can open it until an
// administrator adds a group.
func (s *Service) CreateTeamFolder(ctx context.Context, actor uuid.UUID, name string) (TeamFolder, error) {
	name, err := normalizeName(name)
	if err != nil {
		return TeamFolder{}, err
	}
	t := TeamFolder{ID: newID(), RootID: newID(), Name: name, Groups: []authz.Grant{}, CreatedAt: s.now().UTC()}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockTeamFolders(ctx, tx); err != nil {
			return err
		}
		if taken, err := teamNameTaken(ctx, tx, name, t.ID); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.nodes (id, owner_id, parent_id, kind, name, created_at, updated_at, updated_by, updated_via)
			VALUES ($1, $2, NULL, 'folder', $3, $4, $4, $5, $6)`, t.RootID, t.ID, name, t.CreatedAt, actor, viaFrom(ctx)); err != nil {
			return fmt.Errorf("create team folder root: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.team_folders (id, root_id, created_by, created_at) VALUES ($1, $2, $3, $4)`,
			t.ID, t.RootID, actor, t.CreatedAt); err != nil {
			return fmt.Errorf("create team folder: %w", err)
		}
		return nil
	})
	return t, err
}

// RenameTeamFolder changes the name of a team folder.
func (s *Service) RenameTeamFolder(ctx context.Context, id uuid.UUID, name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockTeamFolders(ctx, tx); err != nil {
			return err
		}
		root, err := s.teamRootOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if taken, err := teamNameTaken(ctx, tx, name, id); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.nodes SET name = $2, updated_at = $3 WHERE id = $1`, root, name, s.now()); err != nil {
			return fmt.Errorf("rename team folder: %w", err)
		}
		return nil
	})
}

// SetTeamFolderQuota sets the storage limit of a team folder; nil removes it.
func (s *Service) SetTeamFolderQuota(ctx context.Context, id uuid.UUID, limit *int64) error {
	if limit != nil && *limit < 0 {
		return &InputError{"Het quotum kan niet negatief zijn."}
	}
	tag, err := s.db.Exec(ctx, `UPDATE drive.team_folders SET limit_bytes = $2 WHERE id = $1`, id, limit)
	if err != nil {
		return fmt.Errorf("set team folder quota: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTeamFolderGroup gives a group a permission on a team folder.
func (s *Service) SetTeamFolderGroup(ctx context.Context, actor, id, groupID uuid.UUID, p authz.Permission) error {
	if !p.Valid() {
		return &InputError{"Kies kijken, bewerken of beheren."}
	}
	root, err := s.teamRootOf(ctx, s.db, id)
	if err != nil {
		return err
	}
	err = s.sharing.Grant(ctx, actor, nodeType, root, authz.Subject{Type: authz.Group, ID: groupID}, p)
	if errors.Is(err, authz.ErrUnknownSubject) {
		return &InputError{"Deze groep bestaat niet (meer)."}
	}
	return err
}

// RemoveTeamFolderGroup takes a group's access to a team folder away.
func (s *Service) RemoveTeamFolderGroup(ctx context.Context, actor, id, groupID uuid.UUID) error {
	root, err := s.teamRootOf(ctx, s.db, id)
	if err != nil {
		return err
	}
	err = s.sharing.Revoke(ctx, actor, nodeType, root, authz.Subject{Type: authz.Group, ID: groupID})
	if errors.Is(err, authz.ErrNoGrant) {
		return ErrNotFound
	}
	return err
}

// DeleteTeamFolder ("opheffen") moves a team folder to the trash: its
// members lose access at once, and after the retention period it is deleted
// for good unless an administrator restores it.
func (s *Service) DeleteTeamFolder(ctx context.Context, id uuid.UUID) error {
	root, err := s.teamRootOf(ctx, s.db, id)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE drive.nodes SET trashed_at = $2 WHERE id = $1 AND trashed_at IS NULL`, root, s.now())
	if err != nil {
		return fmt.Errorf("delete team folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return &InputError{"Deze teammap is al opgeheven."}
	}
	return nil
}

// RestoreTeamFolder takes a team folder out of the trash.
func (s *Service) RestoreTeamFolder(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockTeamFolders(ctx, tx); err != nil {
			return err
		}
		root, err := s.teamRootOf(ctx, tx, id)
		if err != nil {
			return err
		}
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM drive.nodes WHERE id = $1`, root).Scan(&name); err != nil {
			return fmt.Errorf("load team folder: %w", err)
		}
		if taken, err := teamNameTaken(ctx, tx, name, id); err != nil {
			return err
		} else if taken {
			return ErrNameTaken
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.nodes SET trashed_at = NULL WHERE id = $1`, root); err != nil {
			return fmt.Errorf("restore team folder: %w", err)
		}
		return nil
	})
}

// PurgeTeamFolder deletes a team folder that was deleted (opgeheven) for
// good, with everything in it. Two steps are deliberate: one click must
// never destroy a whole team's files.
func (s *Service) PurgeTeamFolder(ctx context.Context, id uuid.UUID) error {
	var tree []uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		root, err := s.teamRootOf(ctx, tx, id)
		if err != nil {
			return err
		}
		var trashed bool
		if err := tx.QueryRow(ctx, `SELECT trashed_at IS NOT NULL FROM drive.nodes WHERE id = $1`, root).Scan(&trashed); err != nil {
			return fmt.Errorf("load team folder: %w", err)
		}
		if !trashed {
			return &InputError{"Hef de teammap eerst op."}
		}
		// Deleting the root also removes the team_folders row (ON DELETE CASCADE).
		// Everything in the team folder (its trash included) lies below the
		// root, and deleting the root also removes the team_folders row.
		tree, err = s.purge(ctx, tx, []uuid.UUID{root})
		return err
	})
	if err == nil {
		s.revokeShares(ctx, tree)
		s.deleteObjectsSoon(ctx)
	}
	return err
}
