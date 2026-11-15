package drive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Trash moves a node (and with it everything inside) to the trash.
func (s *Service) Trash(ctx context.Context, userID, nodeID uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := s.authorize(ctx, tx, userID, nodeID, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.nodes SET trashed_at = $2 WHERE id = $1`, nodeID, s.now()); err != nil {
			return fmt.Errorf("trash node: %w", err)
		}
		return nil
	})
}

// ListTrash returns what the user put in the trash, newest first.
func (s *Service) ListTrash(ctx context.Context, userID uuid.UUID) ([]Node, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+nodeColumns+` FROM drive.nodes
		WHERE owner_id = $1 AND trashed_at IS NOT NULL ORDER BY trashed_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list trash: %w", err)
	}
	return pgx.CollectRows(rows, scanNode)
}

// Restore takes a node out of the trash. When its folder is gone (or in the
// trash itself) it goes to the top level; a name clash gets a suffix.
func (s *Service) Restore(ctx context.Context, userID, nodeID uuid.UUID) (Node, error) {
	var out Node
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		n, err := s.trashRoot(ctx, tx, userID, nodeID)
		if err != nil {
			return err
		}
		if n.ParentID != nil {
			if trashed, err := s.inTrash(ctx, tx, *n.ParentID); err != nil {
				return err
			} else if trashed {
				n.ParentID = nil
			}
		}
		base := n.Name
		for i := 1; ; i++ {
			if _, err := s.childByName(ctx, tx, userID, n.ParentID, n.Name); errors.Is(err, ErrNotFound) {
				break
			} else if err != nil {
				return err
			}
			n.Name = withSuffix(base, i)
		}
		if _, err := tx.Exec(ctx, `UPDATE drive.nodes SET trashed_at = NULL, parent_id = $2, name = $3 WHERE id = $1`,
			nodeID, n.ParentID, n.Name); err != nil {
			return fmt.Errorf("restore: %w", err)
		}
		n.TrashedAt = nil
		out = n
		return nil
	})
	return out, err
}

// withSuffix turns "report.pdf" into "report (hersteld 2).pdf".
func withSuffix(name string, i int) string {
	ext := ""
	if dot := strings.LastIndex(name, "."); dot > 0 { // ".bashrc" has no extension
		name, ext = name[:dot], name[dot:]
	}
	if i == 1 {
		return name + " (hersteld)" + ext
	}
	return fmt.Sprintf("%s (hersteld %d)%s", name, i, ext)
}

// trashRoot loads a node the user put in the trash themselves.
func (s *Service) trashRoot(ctx context.Context, q dbtx, userID, nodeID uuid.UUID) (Node, error) {
	rows, err := q.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = $1 AND owner_id = $2 AND trashed_at IS NOT NULL`,
		nodeID, userID)
	if err != nil {
		return Node{}, fmt.Errorf("load trashed node: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

// purge deletes nodes for good: their rows (with everything below them) go,
// and their objects are scheduled for deletion in the same transaction.
func (s *Service) purge(ctx context.Context, tx pgx.Tx, nodeIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id FROM drive.nodes WHERE id = ANY($1)
			UNION ALL
			SELECT n.id FROM drive.nodes n JOIN tree t ON n.parent_id = t.id)
		INSERT INTO drive.object_deletions (object_key, next_attempt_at)
		SELECT v.object_key, $2 FROM drive.versions v JOIN tree t ON v.node_id = t.id
		ON CONFLICT DO NOTHING`, nodeIDs, s.now()); err != nil {
		return fmt.Errorf("schedule object deletion: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM drive.nodes WHERE id = ANY($1)`, nodeIDs); err != nil {
		return fmt.Errorf("delete nodes: %w", err)
	}
	return nil
}

// DeleteForever removes one node from the trash permanently.
func (s *Service) DeleteForever(ctx context.Context, userID, nodeID uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := s.trashRoot(ctx, tx, userID, nodeID); err != nil {
			return err
		}
		return s.purge(ctx, tx, []uuid.UUID{nodeID})
	})
	if err == nil {
		s.deleteObjectsSoon(ctx)
	}
	return err
}

// EmptyTrash removes everything in the user's trash permanently.
func (s *Service) EmptyTrash(ctx context.Context, userID uuid.UUID) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM drive.nodes WHERE owner_id = $1 AND trashed_at IS NOT NULL`, userID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		return s.purge(ctx, tx, ids)
	})
	if err == nil {
		s.deleteObjectsSoon(ctx)
	}
	return err
}

// purgeExpiredTrash empties trash older than the retention period.
func (s *Service) purgeExpiredTrash(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM drive.nodes WHERE trashed_at < $1`, s.now().Add(-trashRetention))
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(ids) == 0 {
			return err
		}
		s.log.Info("purging expired trash", "nodes", len(ids))
		return s.purge(ctx, tx, ids)
	})
}

// deleteObjectsSoon deletes scheduled objects in the background, so the
// user does not wait for S3; the periodic run retries what fails.
func (s *Service) deleteObjectsSoon(ctx context.Context) {
	go func() {
		if err := s.deleteObjects(context.WithoutCancel(ctx)); err != nil {
			s.log.Warn("delete unreferenced objects", "error", err)
		}
	}()
}

// deleteObjects removes scheduled objects from S3. Each row is claimed with
// SKIP LOCKED, so several instances can work on the queue at once.
func (s *Service) deleteObjects(ctx context.Context) error {
	for {
		done := false
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var key string
			var attempts int
			err := tx.QueryRow(ctx, `
				SELECT object_key, attempts FROM drive.object_deletions
				WHERE next_attempt_at <= $1 ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED`, s.now()).Scan(&key, &attempts)
			if errors.Is(err, pgx.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if delErr := s.objects.Delete(ctx, key); delErr != nil {
				backoff := time.Minute << min(attempts, 10)
				s.log.Warn("object deletion failed, will retry", "key", key, "error", delErr)
				_, err = tx.Exec(ctx, `UPDATE drive.object_deletions SET attempts = attempts + 1, next_attempt_at = $2 WHERE object_key = $1`,
					key, s.now().Add(backoff))
				return err
			}
			_, err = tx.Exec(ctx, `DELETE FROM drive.object_deletions WHERE object_key = $1`, key)
			return err
		})
		if err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
		if done {
			return nil
		}
	}
}
