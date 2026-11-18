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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/drive"
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

// sync brings the index in step with drive: what is new or changed is
// indexed (at most filesPerRound per round), what is gone is forgotten.
func (s *Service) sync(ctx context.Context) error {
	c, model, modelID, err := s.embedder(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return s.purgeOtherModels(ctx, "")
	}
	if err != nil {
		return err
	}
	if err := s.purgeOtherModels(ctx, modelID); err != nil {
		return err
	}
	type state struct {
		version uuid.UUID
		model   string
		failed  *time.Time
	}
	budget := filesPerRound
	after := uuid.Nil
	for {
		refs, err := s.source.TextIndex(ctx, after, indexPage)
		if err != nil {
			return err
		}
		// What the index knows in this range of IDs.
		last := uuid.Max
		if len(refs) == indexPage {
			last = refs[len(refs)-1].NodeID
		}
		rows, err := s.db.Query(ctx, `
			SELECT node_id, version_id, model, CASE WHEN error IS NULL THEN NULL ELSE indexed_at END
			FROM assistant.documents WHERE node_id > $1 AND node_id <= $2`, after, last)
		if err != nil {
			return fmt.Errorf("load index state: %w", err)
		}
		known := map[uuid.UUID]state{}
		var id uuid.UUID
		var st state
		if _, err := pgx.ForEachRow(rows, []any{&id, &st.version, &st.model, &st.failed}, func() error {
			known[id] = st
			return nil
		}); err != nil {
			return fmt.Errorf("load index state: %w", err)
		}
		present := map[uuid.UUID]bool{}
		for _, r := range refs {
			present[r.NodeID] = true
			k, ok := known[r.NodeID]
			current := ok && k.version == r.VersionID && k.model == modelID
			if current && (k.failed == nil || s.now().Sub(*k.failed) < retryFailed) {
				continue
			}
			if budget == 0 {
				s.Kick() // more to do: carry on soon
				return nil
			}
			budget--
			if err := s.indexFile(ctx, c, model, modelID, r); err != nil {
				return err
			}
		}
		for id := range known {
			if !present[id] {
				if err := s.forget(ctx, id); err != nil {
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

// purgeOtherModels removes vectors and index state of any model other than
// current ("" when embeddings are off): search only uses the current one,
// and vectors are derived from users' files, so unused ones do not stay.
func (s *Service) purgeOtherModels(ctx context.Context, current string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE model <> $1`, current); err != nil {
			return fmt.Errorf("purge old vectors: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.documents WHERE model <> $1`, current); err != nil {
			return fmt.Errorf("purge old index state: %w", err)
		}
		return nil
	})
}

// indexFile (re)indexes one file. A failure of the provider is recorded on
// the file and tried again later; only database errors stop the round.
func (s *Service) indexFile(ctx context.Context, c *client, model, modelID string, ref drive.TextRef) error {
	got, text, err := s.source.Text(ctx, ref.NodeID)
	if errors.Is(err, drive.ErrNotFound) {
		return s.forget(ctx, ref.NodeID)
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
				s.log.Warn("assistant embedding failed", "node", ref.NodeID, "error", pe.Detail())
				return s.recordFailure(ctx, got, modelID, pe.Error())
			}
			return err
		}
		vectors = append(vectors, v...)
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE node_id = $1`, ref.NodeID); err != nil {
			return fmt.Errorf("replace chunks: %w", err)
		}
		for i, sp := range spans {
			if _, err := tx.Exec(ctx, `
				INSERT INTO assistant.chunks (node_id, model, chunk_no, start_at, end_at, embedding)
				VALUES ($1, $2, $3, $4, $5, $6::vector)`, ref.NodeID, modelID, i, sp.start, sp.end, vectorLiteral(vectors[i])); err != nil {
				return fmt.Errorf("store chunk: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO assistant.documents (node_id, version_id, model, chunks, indexed_at, error)
			VALUES ($1, $2, $3, $4, $5, NULL)
			ON CONFLICT (node_id) DO UPDATE SET version_id = EXCLUDED.version_id, model = EXCLUDED.model,
				chunks = EXCLUDED.chunks, indexed_at = EXCLUDED.indexed_at, error = NULL`,
			ref.NodeID, got.VersionID, modelID, len(spans), s.now())
		if err != nil {
			return fmt.Errorf("store index state: %w", err)
		}
		return nil
	})
}

func (s *Service) recordFailure(ctx context.Context, ref drive.TextRef, modelID, reason string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO assistant.documents (node_id, version_id, model, chunks, indexed_at, error)
		VALUES ($1, $2, $3, 0, $4, $5)
		ON CONFLICT (node_id) DO UPDATE SET version_id = EXCLUDED.version_id, model = EXCLUDED.model,
			chunks = 0, indexed_at = EXCLUDED.indexed_at, error = EXCLUDED.error`,
		ref.NodeID, ref.VersionID, modelID, s.now(), reason)
	if err != nil {
		return fmt.Errorf("record index failure: %w", err)
	}
	return nil
}

// forget removes a file that drive no longer has from the index.
func (s *Service) forget(ctx context.Context, nodeID uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.chunks WHERE node_id = $1`, nodeID); err != nil {
			return fmt.Errorf("forget chunks: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant.documents WHERE node_id = $1`, nodeID); err != nil {
			return fmt.Errorf("forget document: %w", err)
		}
		return nil
	})
}

// IndexStatus says how far the index is, for the settings page.
type IndexStatus struct {
	Configured bool   `json:"configured"`
	Files      int    `json:"files"`   // files with text in drive
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
	if out.Files, err = s.source.TextCount(ctx); err != nil {
		return IndexStatus{}, err
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
