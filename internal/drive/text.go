package drive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TextRef names the extracted text of one file version, and when it was
// read: a file is read again (same version, new text) when extraction
// learns new file types.
type TextRef struct {
	NodeID    uuid.UUID
	VersionID uuid.UUID
	ReadAt    time.Time
}

// TextIndex lists which files have extracted text, and of which version,
// ordered by file ID and starting after after (uuid.Nil for the start). It
// lets another module (the assistant) keep its own index in step: anything
// new or changed it indexes, anything missing it forgets.
func (s *Service) TextIndex(ctx context.Context, after uuid.UUID, limit int) ([]TextRef, error) {
	rows, err := s.db.Query(ctx, `
		SELECT node_id, version_id, indexed_at FROM drive.contents WHERE node_id > $1 ORDER BY node_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list texts: %w", err)
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TextRef, error) {
		var r TextRef
		err := row.Scan(&r.NodeID, &r.VersionID, &r.ReadAt)
		r.ReadAt = r.ReadAt.UTC()
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("list texts: %w", err)
	}
	return refs, nil
}

// Text returns the extracted text of a file and the version it comes from.
// It checks no permissions: it is for indexing only. Whatever reaches a
// user goes through Visible first.
func (s *Service) Text(ctx context.Context, nodeID uuid.UUID) (TextRef, string, error) {
	ref := TextRef{NodeID: nodeID}
	var body string
	err := s.db.QueryRow(ctx, `SELECT version_id, indexed_at, body FROM drive.contents WHERE node_id = $1`, nodeID).Scan(&ref.VersionID, &ref.ReadAt, &body)
	ref.ReadAt = ref.ReadAt.UTC()
	if errors.Is(err, pgx.ErrNoRows) {
		return TextRef{}, "", ErrNotFound
	}
	if err != nil {
		return TextRef{}, "", fmt.Errorf("load text: %w", err)
	}
	return ref, body, nil
}

// TextCount returns how many files have extracted text.
func (s *Service) TextCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM drive.contents`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count texts: %w", err)
	}
	return n, nil
}
