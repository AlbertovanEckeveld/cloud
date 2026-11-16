package drive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

// Version is one stored content of a file. Every upload of an existing file
// adds a version; its object in S3 never changes (ADR 0009).
type Version struct {
	ID            uuid.UUID `json:"id"`
	Size          int64     `json:"size"`
	ContentType   string    `json:"content_type"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     uuid.UUID `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	Current       bool      `json:"current"`
}

// file loads a live file the user holds at least need on.
func (s *Service) file(ctx context.Context, q dbtx, userID, nodeID uuid.UUID, need authz.Permission) (Node, error) {
	n, err := s.authorize(ctx, q, userID, nodeID, need, false)
	if err != nil {
		return Node{}, err
	}
	if n.Kind != "file" || n.currentVersionID == nil {
		return Node{}, ErrNotFound
	}
	return n, nil
}

// Versions lists a file's versions, newest first.
func (s *Service) Versions(ctx context.Context, userID, nodeID uuid.UUID) ([]Version, error) {
	n, err := s.file(ctx, s.db, userID, nodeID, authz.View)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, size, content_type, created_at, created_by FROM drive.versions
		WHERE node_id = $1 ORDER BY created_at DESC, id DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	versions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Version, error) {
		var v Version
		err := row.Scan(&v.ID, &v.Size, &v.ContentType, &v.CreatedAt, &v.CreatedBy)
		v.CreatedAt = v.CreatedAt.UTC()
		v.Current = v.ID == *n.currentVersionID
		return v, err
	})
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	people := make([]uuid.UUID, len(versions))
	for i, v := range versions {
		people[i] = v.CreatedBy
	}
	names, err := s.sharing.DisplayNames(ctx, people)
	if err != nil {
		return nil, fmt.Errorf("load uploader names: %w", err)
	}
	for i := range versions {
		versions[i].CreatedByName = names[versions[i].CreatedBy]
	}
	return versions, nil
}

// lockNode serialises version changes of one file, so a concurrent restore
// cannot make a version current while it is being deleted.
func (s *Service) lockNode(ctx context.Context, tx pgx.Tx, nodeID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM drive.nodes WHERE id = $1 FOR UPDATE`, nodeID); err != nil {
		return fmt.Errorf("lock node: %w", err)
	}
	return nil
}

// version loads one version of a file.
func (s *Service) version(ctx context.Context, q dbtx, nodeID, versionID uuid.UUID) (Version, string, error) {
	var v Version
	var key string
	err := q.QueryRow(ctx, `
		SELECT id, size, content_type, created_at, created_by, object_key FROM drive.versions
		WHERE id = $1 AND node_id = $2`, versionID, nodeID).Scan(&v.ID, &v.Size, &v.ContentType, &v.CreatedAt, &v.CreatedBy, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, "", ErrNotFound
	}
	if err != nil {
		return Version{}, "", fmt.Errorf("load version: %w", err)
	}
	v.CreatedAt = v.CreatedAt.UTC()
	return v, key, nil
}

// OpenVersion returns the content of one version of a file.
func (s *Service) OpenVersion(ctx context.Context, userID, nodeID, versionID uuid.UUID) (Node, Version, storage.Object, error) {
	n, err := s.file(ctx, s.db, userID, nodeID, authz.View)
	if err != nil {
		return Node{}, Version{}, nil, err
	}
	v, key, err := s.version(ctx, s.db, nodeID, versionID)
	if err != nil {
		return Node{}, Version{}, nil, err
	}
	obj, _, err := s.objects.Open(ctx, key)
	if err != nil {
		return Node{}, Version{}, nil, err
	}
	return n, v, obj, nil
}

// RestoreVersion makes an older version the current content again. The
// version itself is reused rather than copied: objects are immutable, so
// pointing at it is enough, and it costs no storage. The history keeps
// every version, so restoring can itself be undone.
func (s *Service) RestoreVersion(ctx context.Context, userID, nodeID, versionID uuid.UUID) (Node, error) {
	var out Node
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.lockNode(ctx, tx, nodeID); err != nil {
			return err
		}
		n, err := s.file(ctx, tx, userID, nodeID, authz.Edit)
		if err != nil {
			return err
		}
		v, _, err := s.version(ctx, tx, nodeID, versionID)
		if err != nil {
			return err
		}
		n.Size, n.ContentType, n.UpdatedAt, n.currentVersionID, n.UpdatedBy = v.Size, v.ContentType, s.now().UTC(), &v.ID, &userID
		if _, err := tx.Exec(ctx, `
			UPDATE drive.nodes SET current_version_id = $2, size = $3, content_type = $4, updated_at = $5, updated_by = $6 WHERE id = $1`,
			nodeID, v.ID, v.Size, v.ContentType, n.UpdatedAt, userID); err != nil {
			return fmt.Errorf("restore version: %w", err)
		}
		out = n
		return nil
	})
	return out, err
}

// DeleteVersion removes an old version for good, freeing its storage. The
// current version cannot be deleted (trash the file instead). Deleting
// history is destructive, so it needs manage, not just edit.
func (s *Service) DeleteVersion(ctx context.Context, userID, nodeID, versionID uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.lockNode(ctx, tx, nodeID); err != nil {
			return err
		}
		n, err := s.file(ctx, tx, userID, nodeID, authz.Manage)
		if err != nil {
			return err
		}
		if *n.currentVersionID == versionID {
			return &InputError{"De huidige versie kun je niet verwijderen."}
		}
		_, key, err := s.version(ctx, tx, nodeID, versionID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM drive.versions WHERE id = $1`, versionID); err != nil {
			return fmt.Errorf("delete version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.object_deletions (object_key, next_attempt_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			key, s.now()); err != nil {
			return fmt.Errorf("schedule object deletion: %w", err)
		}
		return nil
	})
	if err == nil {
		s.deleteObjectsSoon(ctx)
	}
	return err
}
