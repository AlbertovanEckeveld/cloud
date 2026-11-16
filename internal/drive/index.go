package drive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	indexLease    = 10 * time.Minute // a claimed job is retried after this
	indexAttempts = 3
	indexBatch    = 20
)

// indexDue extracts text from queued file versions (ADR 0017). Each job is
// claimed with a lease rather than a held lock, so a slow PDF never keeps a
// transaction open and a crashed instance's job is picked up again later.
func (s *Service) indexDue(ctx context.Context) error {
	for range indexBatch {
		var version, node uuid.UUID
		var attempts int
		err := s.db.QueryRow(ctx, `
			UPDATE drive.index_queue SET next_attempt_at = $2, attempts = attempts + 1
			WHERE version_id = (SELECT version_id FROM drive.index_queue WHERE next_attempt_at <= $1
			                    ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED)
			RETURNING version_id, node_id, attempts`, s.now(), s.now().Add(indexLease)).Scan(&version, &node, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim index job: %w", err)
		}
		if err := s.indexOne(ctx, node, version); err != nil {
			if attempts < indexAttempts {
				backoff := time.Minute << (2 * attempts)
				_, _ = s.db.Exec(ctx, `UPDATE drive.index_queue SET next_attempt_at = $2 WHERE version_id = $1`, version, s.now().Add(backoff))
				continue
			}
			s.log.Warn("giving up indexing a file", "node", node, "error", err)
		}
		if _, err := s.db.Exec(ctx, `DELETE FROM drive.index_queue WHERE version_id = $1`, version); err != nil {
			return fmt.Errorf("finish index job: %w", err)
		}
	}
	return nil
}

// indexSoon wakes the background loop, so new text is searchable within
// seconds instead of at the next minute.
func (s *Service) indexSoon() {
	select {
	case s.indexKick <- struct{}{}:
	default:
	}
}

func (s *Service) indexOne(ctx context.Context, nodeID, versionID uuid.UUID) error {
	var name, key string
	var size int64
	var current *uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT n.name, n.current_version_id, v.size, v.object_key
		FROM drive.nodes n JOIN drive.versions v ON v.id = $2 WHERE n.id = $1`, nodeID, versionID).Scan(&name, &current, &size, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return fmt.Errorf("load file: %w", err)
	}
	if current == nil || *current != versionID {
		return nil // a newer version is queued too
	}
	text := ""
	if size <= maxIndexFile {
		obj, _, err := s.objects.Open(ctx, key)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		_, err = io.Copy(&buf, io.LimitReader(obj, maxIndexFile))
		obj.Close()
		if err != nil {
			return err
		}
		text, err = s.extractText(ctx, name, buf.Bytes())
		switch {
		case errors.Is(err, errNotIndexable):
			text = ""
		case err != nil:
			return err
		}
	}
	// Even without text the row is kept: it marks this version as done.
	if _, err := s.db.Exec(ctx, `
		INSERT INTO drive.contents (node_id, version_id, body, indexed_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (node_id) DO UPDATE SET version_id = excluded.version_id, body = excluded.body, indexed_at = excluded.indexed_at`,
		nodeID, versionID, text, s.now()); err != nil {
		return fmt.Errorf("store text: %w", err)
	}
	return nil
}
