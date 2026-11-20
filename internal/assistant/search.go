package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
)

const (
	semanticCandidates = 200  // nearest chunks considered per question
	semanticItems      = 20   // items kept from them
	semanticMargin     = 0.12 // an item must be this close to the best match
	rrfK               = 60   // the usual constant of Reciprocal Rank Fusion
	resultLimit        = 50
	snippetChars       = 220
)

// key names an item across sources.
type key struct{ source, id string }

// hit is a chunk near a question. It checks no permissions yet.
type hit struct {
	key
	chunk    int
	start    int
	end      int
	stamp    string // the reading of the text the positions belong to
	distance float64
}

// nearest returns the chunks closest to a question, nearest first, at most
// perItem per item, from the given sources (all when empty).
func (s *Service) nearest(ctx context.Context, question string, perItem int, only []string) ([]hit, error) {
	c, model, modelID, err := s.embedder(ctx)
	if err != nil {
		return nil, err
	}
	v, err := c.Embed(ctx, model, []string{question})
	if err != nil {
		return nil, err
	}
	if len(only) == 0 {
		only = s.sourceNames()
	}
	// Exact search: fast enough for a home's content without an approximate
	// index, and right for any model's vector size.
	rows, err := s.db.Query(ctx, `
		SELECT c.source, c.item_id, c.chunk_no, c.start_at, c.end_at, d.stamp, c.embedding <=> $1::vector
		FROM assistant.chunks c JOIN assistant.documents d ON d.source = c.source AND d.item_id = c.item_id
		WHERE c.model = $2 AND d.model = $2 AND c.source = ANY($4)
		ORDER BY 7 LIMIT $3`, vectorLiteral(v[0]), modelID, semanticCandidates, only)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		return h, row.Scan(&h.source, &h.id, &h.chunk, &h.start, &h.end, &h.stamp, &h.distance)
	})
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	perKey := map[key]int{}
	var out []hit
	for _, h := range all {
		if perKey[h.key] >= perItem {
			continue
		}
		perKey[h.key]++
		out = append(out, h)
	}
	return out, nil
}

// visible returns, of the given items, those the user may see now, asking
// each item's own source.
func (s *Service) visible(ctx context.Context, user uuid.UUID, keys []key) (map[key]knowledge.Item, error) {
	bySource := map[string][]string{}
	for _, k := range keys {
		bySource[k.source] = append(bySource[k.source], k.id)
	}
	out := map[key]knowledge.Item{}
	for name, ids := range bySource {
		src, ok := s.byName[name]
		if !ok {
			continue // a source that is no longer registered shows nothing
		}
		items, err := src.Visible(ctx, user, ids)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			it.Source = name
			out[key{name, it.ID}] = it
		}
	}
	return out, nil
}

// text returns a chunk's text if the source still has the reading it was
// indexed from (positions are only valid for that one).
func (s *Service) text(ctx context.Context, h hit) (string, bool) {
	src, ok := s.byName[h.source]
	if !ok {
		return "", false
	}
	ref, body, err := src.Text(ctx, h.id)
	if err != nil || ref.Stamp != h.stamp || h.end > len(body) {
		return "", false
	}
	return body[h.start:h.end], true
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

// wordSearch asks each source (only those named, all when empty) to search
// by name and words, and returns one list per source, best first.
func (s *Service) wordSearch(ctx context.Context, user uuid.UUID, query string, only []string) ([][]knowledge.Item, error) {
	var lists [][]knowledge.Item
	for _, src := range s.sources {
		if len(only) > 0 && !slices.Contains(only, src.Name()) {
			continue
		}
		items, err := src.Search(ctx, user, query)
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].Source = src.Name()
		}
		lists = append(lists, items)
	}
	return lists, nil
}

// Search finds what the user may see by name, by words and, with an
// embedding model configured, by meaning, in the given sources (all when
// empty). Without a working model it is the sources' own search.
func (s *Service) Search(ctx context.Context, user uuid.UUID, query string, only []string) ([]knowledge.Item, error) {
	lists, err := s.wordSearch(ctx, user, query, only)
	if err != nil {
		return nil, err
	}
	var hits []hit
	if utf8.RuneCountInString(strings.TrimSpace(query)) >= 3 {
		hits, err = s.nearest(ctx, query, 1, only)
		switch {
		case errors.Is(err, ErrNotConfigured):
			hits = nil
		case err != nil:
			// Searching by name and words must keep working when the model does not.
			s.log.Warn("assistant semantic search unavailable", "error", logDetail(err))
			hits = nil
		}
	}
	if len(hits) > 0 {
		cut := hits[0].distance + semanticMargin
		hits = slices.DeleteFunc(hits, func(h hit) bool { return h.distance > cut })
		if len(hits) > semanticItems {
			hits = hits[:semanticItems]
		}
	}
	keys := make([]key, len(hits))
	for i, h := range hits {
		keys[i] = h.key
	}
	allowed, err := s.visible(ctx, user, keys)
	if err != nil {
		return nil, err
	}

	// Reciprocal Rank Fusion: each list adds 1/(k+rank); being found by
	// words and by meaning puts an item on top.
	score := map[key]float64{}
	byKey := map[key]knowledge.Item{}
	for _, list := range lists {
		for i, it := range list {
			k := key{it.Source, it.ID}
			score[k] += 1.0 / float64(rrfK+i+1)
			byKey[k] = it
		}
	}
	rank := 0
	for _, h := range hits {
		it, ok := allowed[h.key]
		if !ok {
			continue // not this user's to see
		}
		rank++
		score[h.key] += 1.0 / float64(rrfK+rank)
		if _, found := byKey[h.key]; !found {
			if t, ok := s.text(ctx, h); ok {
				it.Snippet = excerpt(t)
			}
			byKey[h.key] = it
		}
	}
	out := make([]knowledge.Item, 0, len(byKey))
	for _, it := range byKey {
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b knowledge.Item) int {
		sa, sb := score[key{a.Source, a.ID}], score[key{b.Source, b.ID}]
		switch {
		case sa > sb:
			return -1
		case sa < sb:
			return 1
		}
		return strings.Compare(a.Title, b.Title)
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
