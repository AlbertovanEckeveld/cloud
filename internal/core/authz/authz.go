// Package authz holds the types of the sharing model (ADR 0006) that modules
// use to ask the identity module who may do what. Identity implements
// Authorizer; other modules only depend on this package, not on identity.
package authz

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors returned by an Authorizer.
var (
	ErrInvalidPermission = errors.New("invalid permission")
	ErrUnknownSubject    = errors.New("unknown or disabled user, or unknown group")
	ErrNoGrant           = errors.New("no such share")
)

// Permission is what a user may do with a shared resource. Each level
// includes the ones below it: view < edit < manage.
type Permission string

const (
	None   Permission = ""
	View   Permission = "view"   // see and download
	Edit   Permission = "edit"   // also change, upload, rename, delete
	Manage Permission = "manage" // also share with others
)

var rank = map[Permission]int{None: 0, View: 1, Edit: 2, Manage: 3}

// Valid reports whether p is one of the defined levels (None excluded).
func (p Permission) Valid() bool { return rank[p] > 0 }

// Allows reports whether p includes need.
func (p Permission) Allows(need Permission) bool { return rank[p] >= rank[need] }

// Max returns the stronger of two permissions.
func Max(a, b Permission) Permission {
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// SubjectType is who a resource is shared with.
type SubjectType string

const (
	User  SubjectType = "user"
	Group SubjectType = "group"
)

// Subject is a user or a group.
type Subject struct {
	Type SubjectType `json:"type"`
	ID   uuid.UUID   `json:"id"`
}

// Grant is one share of a resource.
type Grant struct {
	Subject    Subject    `json:"subject"`
	Name       string     `json:"name"` // display name of the user or group
	Permission Permission `json:"permission"`
	GrantedBy  uuid.UUID  `json:"granted_by"`
	GrantedAt  time.Time  `json:"granted_at"`
}

// Shared is a resource shared with a user, directly or through a group.
type Shared struct {
	ResourceID uuid.UUID  `json:"resource_id"`
	Permission Permission `json:"permission"`
	GrantedBy  uuid.UUID  `json:"granted_by"`
}

// Authorizer is the central sharing check. resourceType names the kind of
// resource, e.g. "drive.node"; resource IDs belong to the calling module.
type Authorizer interface {
	// Permissions returns, per resource, the strongest permission the user
	// holds directly or through a group. Resources without any are absent.
	Permissions(ctx context.Context, userID uuid.UUID, resourceType string, ids []uuid.UUID) (map[uuid.UUID]Permission, error)
	// Shared reports which of the resources are shared with anyone.
	Shared(ctx context.Context, resourceType string, ids []uuid.UUID) (map[uuid.UUID]bool, error)
	// Grants lists the shares of one resource.
	Grants(ctx context.Context, resourceType string, id uuid.UUID) ([]Grant, error)
	// Grant shares a resource (or changes the level of an existing share).
	// It fails with ErrInvalidPermission or ErrUnknownSubject.
	Grant(ctx context.Context, actor uuid.UUID, resourceType string, id uuid.UUID, to Subject, p Permission) error
	// Revoke removes one share; ErrNoGrant if there is none.
	Revoke(ctx context.Context, actor uuid.UUID, resourceType string, id uuid.UUID, from Subject) error
	// RevokeAll removes every share of resources that no longer exist.
	RevokeAll(ctx context.Context, resourceType string, ids []uuid.UUID) error
	// SharedWith lists the resources shared with a user.
	SharedWith(ctx context.Context, userID uuid.UUID, resourceType string) ([]Shared, error)
	// DisplayNames returns the display names of users, for showing who
	// shared or changed something. Unknown IDs are absent.
	DisplayNames(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error)
}
