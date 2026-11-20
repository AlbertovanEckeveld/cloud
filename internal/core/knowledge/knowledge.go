// Package knowledge is how modules offer their content to the assistant
// (ADR 0024). A module implements Source; the assistant indexes, searches
// and reads through it, and never touches the module's tables. Every method
// that returns something for a user checks that user's permissions.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Text for an item that no longer exists.
var ErrNotFound = errors.New("knowledge: not found")

// Ref names an item with text. Stamp changes whenever the item's text does
// (a new version, or the same version read again), so the assistant knows
// to index it again. Both are opaque to the assistant.
type Ref struct {
	ID    string
	Stamp string
}

// Item is something a user may see: a file, a folder, and later a note, an
// event, a photo, a message.
type Item struct {
	Source   string `json:"source"`   // the Source's name
	ID       string `json:"id"`       // unique within the source
	Kind     string `json:"kind"`     // file, folder, …
	Title    string `json:"title"`    // what to show
	Location string `json:"location"` // where it is, as the user knows it
	Link     string `json:"link"`     // the page that shows it
	Snippet  string `json:"snippet,omitempty"`
	// Detail is the source's own description (for drive: the file's node),
	// so a page can show it the way that module does.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Source is one module's content.
type Source interface {
	// Name identifies the source in the index and in items: drive, pim, …
	Name() string
	// Index lists items that have text, ordered by ID, after after ("" for
	// the start). It checks no permissions: it is for indexing only.
	Index(ctx context.Context, after string, limit int) ([]Ref, error)
	// Count says how many items Index would list in total.
	Count(ctx context.Context) (int, error)
	// Text returns an item's text and its current stamp, without checking
	// permissions: for indexing, and for reading what Visible allowed.
	Text(ctx context.Context, id string) (Ref, string, error)
	// Visible returns, of ids, the items the user may see now.
	Visible(ctx context.Context, user uuid.UUID, ids []string) ([]Item, error)
	// Search finds items by name and words among what the user may see.
	Search(ctx context.Context, user uuid.UUID, query string) ([]Item, error)
}

// Container is offered by sources whose items hold other items (a folder,
// later a notebook or an album), so "what is in here?" can be answered.
type Container interface {
	// Children lists what the item holds, as the user may see it, or
	// ErrNotFound when the user may not see the item or it holds nothing.
	Children(ctx context.Context, user uuid.UUID, id string) ([]Item, error)
}
