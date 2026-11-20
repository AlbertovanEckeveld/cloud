package drive

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/knowledge"
)

// Knowledge is drive as a source for the assistant (ADR 0024): files with
// text, and whatever a user may open.
type Knowledge struct{ s *Service }

// Knowledge returns drive's source for the assistant.
func (s *Service) Knowledge() Knowledge { return Knowledge{s} }

// Name implements knowledge.Source.
func (Knowledge) Name() string { return "drive" }

func stamp(r TextRef) string {
	return r.VersionID.String() + "@" + r.ReadAt.UTC().Format(time.RFC3339Nano)
}

// Index implements knowledge.Source.
func (k Knowledge) Index(ctx context.Context, after string, limit int) ([]knowledge.Ref, error) {
	from := uuid.Nil
	if after != "" {
		id, err := uuid.Parse(after)
		if err != nil {
			return nil, err
		}
		from = id
	}
	refs, err := k.s.TextIndex(ctx, from, limit)
	if err != nil {
		return nil, err
	}
	out := make([]knowledge.Ref, len(refs))
	for i, r := range refs {
		out[i] = knowledge.Ref{ID: r.NodeID.String(), Stamp: stamp(r)}
	}
	return out, nil
}

// Count implements knowledge.Source.
func (k Knowledge) Count(ctx context.Context) (int, error) { return k.s.TextCount(ctx) }

// Text implements knowledge.Source.
func (k Knowledge) Text(ctx context.Context, id string) (knowledge.Ref, string, error) {
	nodeID, err := uuid.Parse(id)
	if err != nil {
		return knowledge.Ref{}, "", knowledge.ErrNotFound
	}
	ref, body, err := k.s.Text(ctx, nodeID)
	if errors.Is(err, ErrNotFound) {
		return knowledge.Ref{}, "", knowledge.ErrNotFound
	}
	if err != nil {
		return knowledge.Ref{}, "", err
	}
	return knowledge.Ref{ID: id, Stamp: stamp(ref)}, body, nil
}

// Visible implements knowledge.Source.
func (k Knowledge) Visible(ctx context.Context, user uuid.UUID, ids []string) ([]knowledge.Item, error) {
	nodes := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if n, err := uuid.Parse(id); err == nil {
			nodes = append(nodes, n)
		}
	}
	found, err := k.s.Visible(ctx, user, nodes)
	if err != nil {
		return nil, err
	}
	return items(found)
}

// Search implements knowledge.Source.
func (k Knowledge) Search(ctx context.Context, user uuid.UUID, query string) ([]knowledge.Item, error) {
	found, err := k.s.Search(ctx, user, query)
	if err != nil {
		return nil, err
	}
	return items(found)
}

func items(results []SearchResult) ([]knowledge.Item, error) {
	out := make([]knowledge.Item, 0, len(results))
	for _, r := range results {
		detail, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		out = append(out, knowledge.Item{
			Source: "drive", ID: r.ID.String(), Kind: r.Kind, Title: r.Name, Location: r.Location,
			Link: linkOf(r), Snippet: r.Snippet, Detail: detail,
		})
	}
	return out, nil
}

// linkOf is the page that shows a result: a folder itself, a file in its folder.
func linkOf(r SearchResult) string {
	switch {
	case r.Kind == "folder":
		return "/files/" + r.ID.String()
	case r.LocationID != nil:
		return "/files/" + r.LocationID.String()
	case r.Location == "Gedeeld met mij":
		return "/files/shared"
	}
	return "/files"
}
