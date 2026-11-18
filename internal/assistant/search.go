package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/drive"
)

const (
	semanticCandidates = 200  // nearest chunks considered per question
	semanticFiles      = 20   // files kept from them
	semanticMargin     = 0.12 // a file must be this close to the best match
	rrfK               = 60   // the usual constant of Reciprocal Rank Fusion
	resultLimit        = 50
	snippetChars       = 220
)

// hit is a chunk near a question.
type hit struct {
	node     uuid.UUID
	chunk    int
	start    int
	end      int
	version  uuid.UUID
	readAt   *time.Time // the reading of the text the positions belong to
	distance float64
}

// matches reports whether the text drive has now is the reading the hit's
// positions were taken from.
func (h hit) matches(ref drive.TextRef) bool {
	return ref.VersionID == h.version && h.readAt != nil && h.readAt.Equal(ref.ReadAt)
}

// nearest returns the chunks closest to a question, nearest first, at most
// one per file unless perFile is larger. It does not check permissions.
func (s *Service) nearest(ctx context.Context, question string, perFile int) ([]hit, error) {
	c, model, modelID, err := s.embedder(ctx)
	if err != nil {
		return nil, err
	}
	v, err := c.Embed(ctx, model, []string{question})
	if err != nil {
		return nil, err
	}
	// Exact search: fast enough for a home's files without an approximate
	// index, and right for any model's vector size.
	rows, err := s.db.Query(ctx, `
		SELECT c.node_id, c.chunk_no, c.start_at, c.end_at, d.version_id, d.text_at, c.embedding <=> $1::vector
		FROM assistant.chunks c JOIN assistant.documents d ON d.node_id = c.node_id
		WHERE c.model = $2 AND d.model = $2
		ORDER BY 7 LIMIT $3`, vectorLiteral(v[0]), modelID, semanticCandidates)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		return h, row.Scan(&h.node, &h.chunk, &h.start, &h.end, &h.version, &h.readAt, &h.distance)
	})
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	perNode := map[uuid.UUID]int{}
	var out []hit
	for _, h := range all {
		if perNode[h.node] >= perFile {
			continue
		}
		perNode[h.node]++
		out = append(out, h)
	}
	return out, nil
}

// excerpt is the start of a chunk as plain text, for a result line.
func excerpt(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= snippetChars {
		return text
	}
	r := []rune(text)[:snippetChars]
	return strings.TrimSpace(string(r)) + " …"
}

// Search finds files by name, by the words in them and, with an embedding
// model configured, by meaning. Every result passed drive's permission
// check. Without a working model it is drive's search.
func (s *Service) Search(ctx context.Context, userID uuid.UUID, query string) ([]drive.SearchResult, error) {
	text, err := s.source.Search(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(strings.TrimSpace(query)) < 3 {
		return text, nil
	}
	hits, err := s.nearest(ctx, query, 1)
	if errors.Is(err, ErrNotConfigured) {
		return text, nil
	}
	if err != nil {
		// Searching by name and words must keep working when the model does not.
		s.log.Warn("assistant semantic search unavailable", "error", logDetail(err))
		return text, nil
	}
	if len(hits) > 0 {
		cut := hits[0].distance + semanticMargin
		hits = slices.DeleteFunc(hits, func(h hit) bool { return h.distance > cut })
		if len(hits) > semanticFiles {
			hits = hits[:semanticFiles]
		}
	}
	ids := make([]uuid.UUID, len(hits))
	for i, h := range hits {
		ids[i] = h.node
	}
	visible, err := s.source.Visible(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	allowed := map[uuid.UUID]drive.SearchResult{}
	for _, v := range visible {
		allowed[v.ID] = v
	}

	// Reciprocal Rank Fusion: each list adds 1/(k+rank); being found by
	// words and by meaning puts a file on top.
	score := map[uuid.UUID]float64{}
	byID := map[uuid.UUID]drive.SearchResult{}
	for i, r := range text {
		score[r.ID] += 1.0 / float64(rrfK+i+1)
		byID[r.ID] = r
	}
	rank := 0
	for _, h := range hits {
		r, ok := allowed[h.node]
		if !ok {
			continue // not this user's to see
		}
		rank++
		score[h.node] += 1.0 / float64(rrfK+rank)
		if _, found := byID[h.node]; !found {
			if ref, body, err := s.source.Text(ctx, h.node); err == nil && h.matches(ref) && h.end <= len(body) {
				r.Snippet = excerpt(body[h.start:h.end])
			}
			byID[h.node] = r
		}
	}
	out := make([]drive.SearchResult, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b drive.SearchResult) int {
		switch {
		case score[a.ID] > score[b.ID]:
			return -1
		case score[a.ID] < score[b.ID]:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(out) > resultLimit {
		out = out[:resultLimit]
	}
	return out, nil
}

// logDetail returns what a provider really said, for the log.
func logDetail(err error) error {
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Detail() != nil {
		return pe.Detail()
	}
	return err
}
