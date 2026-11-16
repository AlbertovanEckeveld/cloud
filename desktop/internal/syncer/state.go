package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite" // database/sql driver "sqlite", pure Go
)

// State is the local bookkeeping in SQLite: per root the cursor, a mirror
// of the server's tree, and the "base": how each path looked after the last
// successful sync. Comparing the disk and the server with the base tells who
// changed what, so offline changes on both sides are reconciled correctly.
type State struct{ db *sql.DB }

// OpenState opens (or creates) the state database. It holds file names and
// structure, so it is readable by its owner only.
func OpenState(path string) (*State, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("protect state: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS roots (root TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT '');
		CREATE TABLE IF NOT EXISTS remote (
			root TEXT NOT NULL, id TEXT NOT NULL, parent TEXT, kind TEXT NOT NULL, name TEXT NOT NULL,
			size INTEGER NOT NULL, sha256 TEXT NOT NULL, PRIMARY KEY (root, id));
		CREATE TABLE IF NOT EXISTS base (
			root TEXT NOT NULL, path TEXT NOT NULL, id TEXT NOT NULL, kind TEXT NOT NULL,
			sha256 TEXT NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, PRIMARY KEY (root, path));`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create state tables: %w", err)
	}
	return &State{db: db}, nil
}

// Close closes the database.
func (s *State) Close() error { return s.db.Close() }

func (s *State) cursor(ctx context.Context, root string) (string, error) {
	var c string
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM roots WHERE root = ?`, root).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return c, err
}

// applyRemote stores a page of server state and its cursor in one
// transaction, so a crash never leaves a cursor ahead of the mirror.
func (s *State) applyRemote(ctx context.Context, root string, p Page, full bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if full {
		if _, err := tx.ExecContext(ctx, `DELETE FROM remote WHERE root = ?`, root); err != nil {
			return err
		}
	}
	for _, it := range p.Items {
		if it.Gone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM remote WHERE root = ? AND id = ?`, root, it.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remote (root, id, parent, kind, name, size, sha256) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (root, id) DO UPDATE SET parent = excluded.parent, kind = excluded.kind, name = excluded.name,
				size = excluded.size, sha256 = excluded.sha256`,
			root, it.ID, it.ParentID, it.Kind, it.Name, it.Size, it.SHA256); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO roots (root, cursor) VALUES (?, ?) ON CONFLICT (root) DO UPDATE SET cursor = excluded.cursor`, root, p.Cursor); err != nil {
		return err
	}
	return tx.Commit()
}

// remoteEntry is a node of the server mirror.
type remoteEntry struct {
	id, kind, sha string
	parent        *string
	name          string
	size          int64
}

func (s *State) remote(ctx context.Context, root string) (map[string]remoteEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, parent, kind, name, size, sha256 FROM remote WHERE root = ?`, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]remoteEntry{}
	for rows.Next() {
		var e remoteEntry
		if err := rows.Scan(&e.id, &e.parent, &e.kind, &e.name, &e.size, &e.sha); err != nil {
			return nil, err
		}
		out[e.id] = e
	}
	return out, rows.Err()
}

// baseEntry is a path as it was after the last successful sync.
type baseEntry struct {
	id, kind, sha string
	size, mtime   int64
}

func (s *State) base(ctx context.Context, root string) (map[string]baseEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, id, kind, sha256, size, mtime FROM base WHERE root = ?`, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]baseEntry{}
	for rows.Next() {
		var p string
		var b baseEntry
		if err := rows.Scan(&p, &b.id, &b.kind, &b.sha, &b.size, &b.mtime); err != nil {
			return nil, err
		}
		out[p] = b
	}
	return out, rows.Err()
}

func (s *State) setBase(ctx context.Context, root, path string, b baseEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO base (root, path, id, kind, sha256, size, mtime) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (root, path) DO UPDATE SET id = excluded.id, kind = excluded.kind, sha256 = excluded.sha256,
			size = excluded.size, mtime = excluded.mtime`, root, path, b.id, b.kind, b.sha, b.size, b.mtime)
	return err
}

func (s *State) dropBase(ctx context.Context, root, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM base WHERE root = ? AND path = ?`, root, path)
	return err
}

// upsertRemote records a node the engine itself just created on the server,
// so later steps of the same run can use it as a parent.
func (s *State) upsertRemote(ctx context.Context, root string, e remoteEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO remote (root, id, parent, kind, name, size, sha256) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (root, id) DO UPDATE SET parent = excluded.parent, kind = excluded.kind, name = excluded.name,
			size = excluded.size, sha256 = excluded.sha256`, root, e.id, e.parent, e.kind, e.name, e.size, e.sha)
	return err
}
