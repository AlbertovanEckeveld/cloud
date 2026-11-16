package drive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/storage"
)

// Upload describes a file being uploaded.
type Upload struct {
	ParentID *uuid.UUID // nil: the top level
	Name     string
	Size     int64     // exact number of bytes the body will contain; -1 if unknown
	Body     io.Reader // the content; read exactly once
	// Replace stores the upload as a new version of an existing file with
	// the same name. Without it, an existing name is an error.
	Replace bool
	// FileID stores a new version of this file (needs edit on the file
	// itself); ParentID and Name are then ignored. Used by the editor.
	FileID *uuid.UUID
}

// countingReader hashes and counts what passes through it.
type countingReader struct {
	r io.Reader
	n int64
	h hash.Hash
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	_, _ = c.h.Write(p[:n])
	return n, err
}

// contentType guesses a type from the extension, else from the first bytes.
// It is only used for display: files are always served as downloads.
func contentType(name string, head []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(name))); t != "" {
		return t
	}
	return http.DetectContentType(head)
}

// checkQuota fails when adding extra bytes would exceed the user's limit.
func (s *Service) checkQuota(ctx context.Context, q dbtx, userID uuid.UUID, extra int64) error {
	used, limit, err := s.usage(ctx, q, userID)
	if err != nil {
		return err
	}
	if limit != nil && used+extra > *limit {
		return ErrQuotaExceeded
	}
	return nil
}

// Store saves an uploaded file: as a new file, or (with Replace) as a new
// version of the existing file with that name. The content is streamed to
// S3 while being hashed; nothing is kept in memory. In a shared folder the
// file belongs to (and counts towards the quota of) the folder's owner; the
// version records who uploaded it.
func (s *Service) Store(ctx context.Context, userID uuid.UUID, up Upload) (Node, error) {
	var owner uuid.UUID
	var perm authz.Permission
	if up.FileID != nil {
		f, err := s.authorize(ctx, s.db, userID, *up.FileID, authz.Edit, false)
		if err != nil {
			return Node{}, err
		}
		if f.Kind != "file" {
			return Node{}, ErrNotFound
		}
		// The real parent (authorize hides it above a share).
		if err := s.db.QueryRow(ctx, `SELECT parent_id FROM drive.nodes WHERE id = $1`, f.ID).Scan(&up.ParentID); err != nil {
			return Node{}, fmt.Errorf("load node: %w", err)
		}
		up.Name, up.Replace, owner, perm = f.Name, true, f.ownerID, f.Permission
	}
	name, err := normalizeName(up.Name)
	if err != nil {
		return Node{}, err
	}
	if up.Size < -1 || up.Size > s.maxFile {
		return Node{}, ErrTooLarge
	}
	if up.FileID == nil {
		if owner, perm, err = s.target(ctx, s.db, userID, up.ParentID); err != nil {
			return Node{}, err
		}
	}
	existing, err := s.childByName(ctx, s.db, owner, up.ParentID, name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Node{}, err
	}
	if err == nil && (!up.Replace || existing.Kind != "file") {
		return Node{}, ErrNameTaken
	}
	// Checked again inside the transaction; this early check avoids
	// streaming gigabytes only to reject them.
	if err := s.checkQuota(ctx, s.db, owner, max(up.Size, 0)); err != nil {
		return Node{}, err
	}

	buffered := bufio.NewReaderSize(up.Body, 512)
	head, _ := buffered.Peek(512)
	ctype := contentType(name, head)
	versionID := newID()
	key := "drive/" + versionID.String()
	limit := up.Size
	if limit < 0 {
		limit = s.maxFile + 1 // unknown length: read one byte too many to notice
	}
	counter := &countingReader{r: io.LimitReader(buffered, limit), h: sha256.New()}
	if err := s.objects.PutStream(ctx, key, counter, up.Size, ctype); err != nil {
		s.forget(ctx, key) // a multipart upload may have left parts behind
		if up.Size >= 0 && counter.n < up.Size {
			// The client sent less than it announced (e.g. it disconnected).
			return Node{}, ErrIncomplete
		}
		return Node{}, err
	}
	switch {
	case up.Size < 0 && counter.n > s.maxFile:
		s.forget(ctx, key)
		return Node{}, ErrTooLarge
	case up.Size >= 0 && counter.n != up.Size:
		s.forget(ctx, key)
		return Node{}, ErrIncomplete
	}
	up.Size = counter.n
	sum := counter.h.Sum(nil)

	var out Node
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.checkQuota(ctx, tx, owner, up.Size); err != nil {
			return err
		}
		now := s.now().UTC()
		node, err := s.childByName(ctx, tx, owner, up.ParentID, name)
		switch {
		case errors.Is(err, ErrNotFound):
			node = Node{ID: newID(), ParentID: up.ParentID, Kind: "file", Name: name, CreatedAt: now, ownerID: owner}
			_, err = tx.Exec(ctx, `
				INSERT INTO drive.nodes (id, owner_id, parent_id, kind, name, created_at, updated_at, updated_by, updated_via)
				VALUES ($1, $2, $3, 'file', $4, $5, $5, $6, $7)`, node.ID, owner, up.ParentID, name, now, userID, viaFrom(ctx))
			if uniqueViolation(err) {
				return ErrNameTaken
			}
		case err == nil && (!up.Replace || node.Kind != "file"):
			return ErrNameTaken // created by a concurrent request
		}
		if err != nil {
			return fmt.Errorf("store node: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO drive.versions (id, node_id, object_key, size, sha256, content_type, created_at, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, versionID, node.ID, key, up.Size, sum, ctype, now, userID); err != nil {
			return fmt.Errorf("store version: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE drive.nodes SET current_version_id = $2, size = $3, content_type = $4, updated_at = $5, updated_by = $6, updated_via = $7
			WHERE id = $1`, node.ID, versionID, up.Size, ctype, now, userID, viaFrom(ctx)); err != nil {
			return fmt.Errorf("update node: %w", err)
		}
		node.Size, node.ContentType, node.UpdatedAt, node.currentVersionID, node.UpdatedBy = up.Size, ctype, now, &versionID, &userID
		out = node
		return nil
	})
	if err != nil {
		s.forget(ctx, key)
		return Node{}, err
	}
	s.indexSoon()
	nodes := []Node{out}
	if err := s.annotate(ctx, userID, perm, nodes); err != nil {
		return Node{}, err
	}
	return nodes[0], nil
}

// forget schedules an object that never got (or no longer has) a database
// row for deletion.
func (s *Service) forget(ctx context.Context, key string) {
	if _, err := s.db.Exec(context.WithoutCancel(ctx), `
		INSERT INTO drive.object_deletions (object_key, next_attempt_at) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		key, s.now()); err != nil {
		s.log.Error("could not schedule object deletion", "key", key, "error", err)
	}
}

// childByName finds a live child of an owner's folder by (case-insensitive) name.
func (s *Service) childByName(ctx context.Context, q dbtx, ownerID uuid.UUID, parentID *uuid.UUID, name string) (Node, error) {
	rows, err := q.Query(ctx, `
		SELECT `+nodeColumns+` FROM drive.nodes
		WHERE owner_id = $1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name) = lower($3) AND trashed_at IS NULL`,
		ownerID, parentID, name)
	if err != nil {
		return Node{}, fmt.Errorf("find by name: %w", err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

// Open returns a file's current content for download.
func (s *Service) Open(ctx context.Context, userID, nodeID uuid.UUID) (Node, storage.Object, error) {
	n, err := s.authorize(ctx, s.db, userID, nodeID, authz.View, false)
	if err != nil {
		return Node{}, nil, err
	}
	if n.Kind != "file" || n.currentVersionID == nil {
		return Node{}, nil, ErrNotFound
	}
	obj, err := s.openVersion(ctx, *n.currentVersionID)
	return n, obj, err
}

func (s *Service) openVersion(ctx context.Context, versionID uuid.UUID) (storage.Object, error) {
	var key string
	if err := s.db.QueryRow(ctx, `SELECT object_key FROM drive.versions WHERE id = $1`, versionID).Scan(&key); err != nil {
		return nil, fmt.Errorf("load version: %w", err)
	}
	obj, _, err := s.objects.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	return obj, nil
}
