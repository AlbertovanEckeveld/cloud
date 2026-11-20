package assistant

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
)

const (
	chunkSize     = 1500      // characters per chunk, roughly 400 tokens
	chunkOverlap  = 200       // characters shared with the previous chunk
	maxChunks     = 400       // per file: about the first 600,000 characters
	embedBatch    = 32        // chunks per embedding request
	indexPage     = 500       // files compared per database round trip
	filesPerRound = 200       // files (re)indexed per round; the rest follows
	retryFailed   = time.Hour // a file that failed is tried again after this
	minChunkChars = 20        // shorter text is not worth a vector
)

// span is a chunk: byte offsets into the file's text.
type span struct{ start, end int }

// chunks cuts text into overlapping pieces of about chunkSize characters,
// ending at white space where possible and never inside a UTF-8 character.
func chunks(text string) []span {
	var out []span
	start := 0
	for start < len(text) && len(out) < maxChunks {
		// Skip leading white space.
		for start < len(text) {
			r, size := utf8.DecodeRuneInString(text[start:])
			if !unicode.IsSpace(r) {
				break
			}
			start += size
		}
		if start >= len(text) {
			break
		}
		end := advance(text, start, chunkSize)
		if end < len(text) {
			// Back up to the last white space in the second half of the chunk.
			for i := end; i > start+(end-start)/2; {
				r, size := utf8.DecodeLastRuneInString(text[:i])
				if unicode.IsSpace(r) {
					end = i
					break
				}
				i -= size
			}
		}
		if strings.TrimSpace(text[start:end]) != "" && utf8.RuneCountInString(text[start:end]) >= minChunkChars {
			out = append(out, span{start, end})
		}
		if end >= len(text) {
			break
		}
		next := back(text, end, chunkOverlap)
		if next <= start {
			next = end
		}
		start = next
	}
	return out
}

// advance moves n characters forward from i.
func advance(s string, i, n int) int {
	for ; n > 0 && i < len(s); n-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// back moves n characters backward from i.
func back(s string, i, n int) int {
	for ; n > 0 && i > 0; n-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return i
}

// vectorLiteral writes a vector in pgvector's text form: [1,2,3].
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// sync brings the index in step with every source: what is new or changed
// is indexed (at most filesPerRound per round), what is gone is forgotten.
func (s *Service) sync(ctx context.Context) error {
	c, model, modelID, err := s.embedder(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return s.purge(ctx, "")
	}
	if err != nil {
		return err
	}
	if err := s.purge(ctx, modelID); err != nil {
		return err
	}
	budget := filesPerRound
	for _, src := range s.sources {
		if err := s.syncSource(ctx, src, c, model, modelID, &budget); err != nil {
			return err
		}
		if budget == 0 {
			s.Kick() // more to do: carry on soon
			return nil
		}
	}
	return nil
}

func (s *Service) syncSource(ctx context.Context, src knowledge.Source, c *client, model, modelID string, budget *int) error {
	type state struct {
		stamp  string
		model  string
		failed *time.Time
	}
	name := src.Name()
	after := ""
	for {
		refs, err := src.Index(ctx, after, indexPage)
		if err != nil {
			return fmt.Errorf("list %s: %w", name, err)
		}
		// What the index knows in this range of IDs: after < id <= last.
		last := ""
		if len(refs) == indexPage {
			last = refs[len(refs)-1].ID
		}
		rows, err := s.db.Query(ctx, `
			SELECT item_id, stamp, model, CASE WHEN error IS NULL THEN NULL ELSE indexed_at END
			FROM assistant.documents WHERE source = $1 AND item_id > $2 AND ($3 = '' OR item_id <= $3)`, name, after, last)
		if err != nil {
			return fmt.Errorf("load index state: %w", err)
		}
		known := map[string]state{}
		var id string
		var st state
		if _, err := pgx.ForEachRow(rows, []any{&id, &st.stamp, &st.model, &st.failed}, func() error {
			known[id] = st
			return nil
		}); err != nil {
			return fmt.Errorf("load index state: %w", err)
		}
		present := map[string]bool{}
		for _, r := range refs {
			present[r.ID] = true
			k, ok := known[r.ID]
			if ok && k.stamp == r.Stamp && k.model == modelID && (k.failed == nil || s.now().Sub(*k.failed) < retryFailed) {
				continue
			}
			if *budget == 0 {
				return nil
			}
			*budget--
			if err := s.indexItem(ctx, src, c, model, modelID, r.ID); err != nil {
				return err
			}
		}
		for id := range known {
			if !present[id] {
				if err := s.forget(ctx, name, id); err != nil {
					return err
				}
			}
		}
		if len(refs) < indexPage {
			return nil
		}
		after = last
	}
}

// purge removes what search no longer uses: vectors of any model other
// than current ("" when embeddings are off), and everything of sources that
// are no longer registered. Vectors are derived from users' content, so
// unused ones do not stay.
func (s *Service) purge(ctx context.Context, current string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE model <> $1 OR source <> ALL($2)`, current, s.sourceNames()); err != nil {
			return fmt.Errorf("purge old vectors: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.documents WHERE model <> $1 OR source <> ALL($2)`, current, s.sourceNames()); err != nil {
			return fmt.Errorf("purge old index state: %w", err)
		}
		return nil
	})
}

// indexItem (re)indexes one item. A failure of the provider is recorded on
// the item and tried again later; only database errors stop the round.
func (s *Service) indexItem(ctx context.Context, src knowledge.Source, c *client, model, modelID, id string) error {
	name := src.Name()
	ref, text, err := src.Text(ctx, id)
	if errors.Is(err, knowledge.ErrNotFound) {
		return s.forget(ctx, name, id)
	}
	if err != nil {
		return err
	}
	spans := chunks(text)
	vectors := make([][]float32, 0, len(spans))
	for i := 0; i < len(spans); i += embedBatch {
		batch := spans[i:min(i+embedBatch, len(spans))]
		inputs := make([]string, len(batch))
		for j, sp := range batch {
			inputs[j] = text[sp.start:sp.end]
		}
		v, err := c.Embed(ctx, model, inputs)
		if err != nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				s.log.Warn("assistant embedding failed", "source", name, "item", id, "error", pe.Detail())
				return s.recordFailure(ctx, name, ref, modelID, pe.Error())
			}
			return err
		}
		vectors = append(vectors, v...)
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE source = $1 AND item_id = $2`, name, id); err != nil {
			return fmt.Errorf("replace chunks: %w", err)
		}
		for i, sp := range spans {
			if _, err := tx.Exec(ctx, `
				INSERT INTO assistant.chunks (source, item_id, model, chunk_no, start_at, end_at, embedding)
				VALUES ($1, $2, $3, $4, $5, $6, $7::vector)`, name, id, modelID, i, sp.start, sp.end, vectorLiteral(vectors[i])); err != nil {
				return fmt.Errorf("store chunk: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO assistant.documents (source, item_id, stamp, model, chunks, indexed_at, error)
			VALUES ($1, $2, $3, $4, $5, $6, NULL)
			ON CONFLICT (source, item_id) DO UPDATE SET stamp = EXCLUDED.stamp, model = EXCLUDED.model,
				chunks = EXCLUDED.chunks, indexed_at = EXCLUDED.indexed_at, error = NULL`,
			name, id, ref.Stamp, modelID, len(spans), s.now())
		if err != nil {
			return fmt.Errorf("store index state: %w", err)
		}
		return nil
	})
}

func (s *Service) recordFailure(ctx context.Context, source string, ref knowledge.Ref, modelID, reason string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO assistant.documents (source, item_id, stamp, model, chunks, indexed_at, error)
		VALUES ($1, $2, $3, $4, 0, $5, $6)
		ON CONFLICT (source, item_id) DO UPDATE SET stamp = EXCLUDED.stamp, model = EXCLUDED.model,
			chunks = 0, indexed_at = EXCLUDED.indexed_at, error = EXCLUDED.error`,
		source, ref.ID, ref.Stamp, modelID, s.now(), reason)
	if err != nil {
		return fmt.Errorf("record index failure: %w", err)
	}
	return nil
}

// forget removes an item that its source no longer has from the index.
func (s *Service) forget(ctx context.Context, source, id string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE source = $1 AND item_id = $2`, source, id); err != nil {
			return fmt.Errorf("forget chunks: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.documents WHERE source = $1 AND item_id = $2`, source, id); err != nil {
			return fmt.Errorf("forget document: %w", err)
		}
		return nil
	})
}

// IndexStatus says how far the index is, for the settings page.
type IndexStatus struct {
	Configured bool   `json:"configured"`
	Files      int    `json:"files"`   // items with text in all sources
	Indexed    int    `json:"indexed"` // of those, indexed with the current model
	Failed     int    `json:"failed"`
	LastError  string `json:"last_error,omitempty"`
}

// Status reports the progress of the index for the current model.
func (s *Service) Status(ctx context.Context) (IndexStatus, error) {
	st, err := s.GetSettings(ctx)
	if err != nil {
		return IndexStatus{}, err
	}
	var out IndexStatus
	for _, src := range s.sources {
		n, err := src.Count(ctx)
		if err != nil {
			return IndexStatus{}, err
		}
		out.Files += n
	}
	if st.EmbedProvider == nil || st.EmbedModel == "" {
		return out, nil
	}
	out.Configured = true
	var lastError *string
	err = s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE error IS NULL), count(*) FILTER (WHERE error IS NOT NULL),
		       (SELECT error FROM assistant.documents WHERE model = $1 AND error IS NOT NULL ORDER BY indexed_at DESC LIMIT 1)
		FROM assistant.documents WHERE model = $1`, embedModelID(*st.EmbedProvider, st.EmbedModel)).
		Scan(&out.Indexed, &out.Failed, &lastError)
	if err != nil {
		return IndexStatus{}, fmt.Errorf("index status: %w", err)
	}
	if lastError != nil {
		out.LastError = *lastError
	}
	return out, nil
}
