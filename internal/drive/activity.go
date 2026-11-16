package drive

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Channels through which a change was made (ADR 0018).
const (
	ViaWeb     = "web"
	ViaDesktop = "desktop"
	ViaWebDAV  = "webdav"
	ViaEditor  = "editor"
)

type viaKey struct{}

// withVia records in ctx through which channel a request changes files.
func withVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

func viaFrom(ctx context.Context) *string {
	if v, ok := ctx.Value(viaKey{}).(string); ok {
		return &v
	}
	return nil
}

// ActivityItem is one change, as the user sees it.
type ActivityItem struct {
	At        time.Time `json:"at"`
	Action    string    `json:"action"` // created, updated, renamed, moved, trashed, restored, deleted
	NodeID    uuid.UUID `json:"node_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	ActorName string    `json:"actor_name"`
	Via       string    `json:"via"`
	// Present is false when the item no longer exists or is in the trash.
	Present bool `json:"present"`
}

const activityLimit = 100

// Activity lists recent changes in everything the user can see. A new file
// shows as one item (not "created" and "updated"); changes to items the user
// cannot see (anymore) are left out, except their own deletions.
func (s *Service) Activity(ctx context.Context, userID uuid.UUID) ([]ActivityItem, error) {
	shared, err := s.sharing.SharedWith(ctx, userID, nodeType)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	roots := make([]uuid.UUID, len(shared))
	for i, sh := range shared {
		roots[i] = sh.ResourceID
	}
	rows, err := s.db.Query(ctx, `
		WITH owners AS (SELECT $1::uuid AS id UNION SELECT owner_id FROM drive.nodes WHERE id = ANY($2)),
		events AS (
			SELECT DISTINCT ON (c.txid, c.node_id) c.at, c.action, c.node_id, c.name, c.actor, c.via, c.owner_id
			FROM drive.changes c
			WHERE c.owner_id IN (SELECT id FROM owners) AND c.action IS NOT NULL AND c.action <> 'changed'
			ORDER BY c.txid, c.node_id, CASE c.action WHEN 'created' THEN 0 ELSE 1 END)
		SELECT e.at, e.action, e.node_id, e.name, e.actor, COALESCE(e.via, ''), e.owner_id, COALESCE(n.kind, '')
		FROM events e LEFT JOIN drive.nodes n ON n.id = e.node_id
		ORDER BY e.at DESC LIMIT $3`, userID, roots, activityLimit*3)
	if err != nil {
		return nil, fmt.Errorf("read activity: %w", err)
	}
	type event struct {
		item  ActivityItem
		actor *uuid.UUID
		owner uuid.UUID
	}
	var events []event
	var ids []uuid.UUID
	for rows.Next() {
		var ev event
		if err := rows.Scan(&ev.item.At, &ev.item.Action, &ev.item.NodeID, &ev.item.Name, &ev.actor, &ev.item.Via, &ev.owner, &ev.item.Kind); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read activity: %w", err)
		}
		ev.item.At = ev.item.At.UTC()
		events = append(events, ev)
		ids = append(ids, ev.item.NodeID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Which of these nodes may the user see now? Same check as opening.
	visible := map[uuid.UUID]bool{}
	for _, id := range uniqueIDs(ids) {
		if _, err := s.authorize(ctx, s.db, userID, id, authz.View, false); err == nil {
			visible[id] = true
		}
	}
	var kept []event
	var actors []uuid.UUID
	for _, ev := range events {
		if len(kept) == activityLimit {
			break
		}
		switch {
		case visible[ev.item.NodeID]:
			ev.item.Present = true
		case ev.owner != userID:
			continue // gone, or not shared (anymore): not ours to show
		}
		kept = append(kept, ev)
		if ev.actor != nil {
			actors = append(actors, *ev.actor)
		}
	}
	names, err := s.sharing.DisplayNames(ctx, uniqueIDs(actors))
	if err != nil {
		return nil, fmt.Errorf("load names: %w", err)
	}
	out := make([]ActivityItem, len(kept))
	for i, ev := range kept {
		out[i] = ev.item
		if ev.actor != nil {
			out[i].ActorName = names[*ev.actor]
		}
	}
	return out, nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (s *Service) handleActivity(w http.ResponseWriter, r *http.Request) {
	items, err := s.Activity(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Items []ActivityItem `json:"items"`
	}{items})
}
