package drive

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

const (
	searchLimit      = 50
	searchCandidates = 500 // name matches checked for access per search
)

// SearchResult is a file or folder found by name, with where it is.
type SearchResult struct {
	Node
	// Location is the folder it is in, as the user knows it: a folder name,
	// or "Mijn bestanden", a team folder, or "Gedeeld met mij".
	Location   string     `json:"location"`
	LocationID *uuid.UUID `json:"location_id"`
	// Snippet shows where the words were found in the file's text, with
	// \x02 and \x03 around each match (never HTML). Empty for name matches.
	Snippet string `json:"snippet,omitempty"`
	rank    float32
}

// Search finds files and folders by (part of) their name in everything the
// user can see: their own files, team folders and what is shared with them,
// with everything below. Items in the trash are never found. Every result
// passes the same permission check as opening it would.
func (s *Service) Search(ctx context.Context, userID uuid.UUID, query string) ([]SearchResult, error) {
	query = norm.NFC.String(strings.TrimSpace(query))
	if utf8.RuneCountInString(query) < 2 {
		return []SearchResult{}, nil
	}
	lower := likeEscape(strings.ToLower(query))

	// Whose trees the user can reach besides their own.
	shared, err := s.sharing.SharedWith(ctx, userID, nodeType)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	roots := make([]uuid.UUID, len(shared))
	for i, sh := range shared {
		roots[i] = sh.ResourceID
	}

	// Name matches (using the trigram index), each with its path upwards.
	rows, err := s.db.Query(ctx, `
		WITH RECURSIVE owners AS (
			SELECT $2::uuid AS id UNION SELECT owner_id FROM drive.nodes WHERE id = ANY($3)),
		cand AS (
			(SELECT id FROM drive.nodes
			 WHERE lower(name) LIKE $1 AND trashed_at IS NULL AND owner_id IN (SELECT id FROM owners)
			 ORDER BY lower(name) = lower($4) DESC, lower(name) LIKE lower($5) DESC, length(name), updated_at DESC
			 LIMIT $6)
			UNION
			-- Words in the text (Dutch and English stemming), best matches first.
			(SELECT c.node_id FROM drive.contents c JOIN drive.nodes n ON n.id = c.node_id
			 WHERE c.tsv @@ (websearch_to_tsquery('dutch', $4) || websearch_to_tsquery('english', $4))
			   AND n.trashed_at IS NULL AND n.owner_id IN (SELECT id FROM owners)
			 ORDER BY ts_rank(c.tsv, websearch_to_tsquery('dutch', $4) || websearch_to_tsquery('english', $4)) DESC
			 LIMIT $6)),
		chain AS (
			SELECT c.id AS cand, n.id, n.parent_id, n.name, n.trashed_at, 0 AS depth FROM cand c JOIN drive.nodes n ON n.id = c.id
			UNION ALL
			SELECT ch.cand, n.id, n.parent_id, n.name, n.trashed_at, ch.depth + 1 FROM chain ch JOIN drive.nodes n ON n.id = ch.parent_id)
		SELECT cand, array_agg(id ORDER BY depth), array_agg(name ORDER BY depth), bool_or(trashed_at IS NOT NULL)
		FROM chain GROUP BY cand`,
		"%"+lower+"%", userID, roots, query, lower+"%", searchCandidates)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	type match struct {
		path  []uuid.UUID
		names []string
	}
	matches := map[uuid.UUID]match{}
	var all []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var m match
		var trashed bool
		if err := rows.Scan(&id, &m.path, &m.names, &trashed); err != nil {
			rows.Close()
			return nil, fmt.Errorf("search: %w", err)
		}
		if !trashed {
			matches[id] = m
			all = append(all, m.path...)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	if len(matches) == 0 {
		return []SearchResult{}, nil
	}

	perms, err := s.sharing.Permissions(ctx, userID, nodeType, all)
	if err != nil {
		return nil, fmt.Errorf("check shares: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(matches))
	for id := range matches {
		ids = append(ids, id)
	}
	nodes, err := s.nodesByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, n := range nodes {
		m := matches[n.ID]
		r := SearchResult{Node: n}
		if n.ownerID == userID {
			r.Permission = authz.Manage
			r.Location = "Mijn bestanden"
			if len(m.path) > 1 {
				r.Location, r.LocationID = m.names[1], &m.path[1]
			}
		} else {
			// The strongest share on the path; the highest one is the access root.
			top := -1
			for i, id := range m.path {
				if p, ok := perms[id]; ok {
					r.Permission = authz.Max(r.Permission, p)
					top = i
				}
			}
			if top < 0 {
				continue // not shared with this user
			}
			switch {
			case top > 0:
				r.Location, r.LocationID = m.names[1], &m.path[1]
			default:
				r.Location = "Gedeeld met mij"
				r.ParentID = nil // the owner's folders above a share stay private
				if team, err := s.teamOf(ctx, n.ownerID); err != nil {
					return nil, err
				} else if team != nil {
					r.Location = "Teammappen"
				}
			}
		}
		out = append(out, r)
	}
	if err := s.snippets(ctx, query, out); err != nil {
		return nil, err
	}
	// Names first (exact, then prefix, then containing, shorter first);
	// then matches in the text, most relevant first.
	q := strings.ToLower(query)
	slices.SortStableFunc(out, func(a, b SearchResult) int {
		rank := func(r SearchResult) int {
			name := strings.ToLower(r.Name)
			switch {
			case name == q:
				return 0
			case strings.HasPrefix(name, q):
				return 1
			case strings.Contains(name, q):
				return 2
			}
			return 3
		}
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		if a.rank != b.rank {
			if a.rank > b.rank {
				return -1
			}
			return 1
		}
		if d := len(a.Name) - len(b.Name); d != 0 {
			return d
		}
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	if len(out) > searchLimit {
		out = out[:searchLimit]
	}
	return out, nil
}

// snippets adds, for results found in their text, where the words were
// found. Markers are control characters the extracted text never contains,
// so the frontend can highlight without ever treating text as HTML.
func (s *Service) snippets(ctx context.Context, query string, results []SearchResult) error {
	if len(results) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	rows, err := s.db.Query(ctx, `
		WITH q AS (SELECT websearch_to_tsquery('dutch', $2) || websearch_to_tsquery('english', $2) AS q)
		SELECT c.node_id, ts_rank(c.tsv, q.q),
		       ts_headline('dutch', c.body, q.q, 'StartSel=`+""+`, StopSel=`+""+`, MaxWords=24, MinWords=10, MaxFragments=2, FragmentDelimiter=" … "')
		FROM drive.contents c, q WHERE c.node_id = ANY($1) AND c.tsv @@ q.q`, ids, query)
	if err != nil {
		return fmt.Errorf("search snippets: %w", err)
	}
	defer rows.Close()
	found := map[uuid.UUID]int{}
	for i, r := range results {
		found[r.ID] = i
	}
	for rows.Next() {
		var id uuid.UUID
		var rank float32
		var snippet string
		if err := rows.Scan(&id, &rank, &snippet); err != nil {
			return fmt.Errorf("search snippets: %w", err)
		}
		if i, ok := found[id]; ok && !strings.Contains(strings.ToLower(results[i].Name), strings.ToLower(query)) {
			results[i].Snippet, results[i].rank = snippet, rank
		}
	}
	return rows.Err()
}

// likeEscape makes user input literal inside a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// nodesByID loads nodes by ID.
func (s *Service) nodesByID(ctx context.Context, ids []uuid.UUID) ([]Node, error) {
	rows, err := s.db.Query(ctx, `SELECT `+nodeColumns+` FROM drive.nodes WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("load nodes: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
