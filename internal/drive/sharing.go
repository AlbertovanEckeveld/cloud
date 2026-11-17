package drive

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

// Shares lists who a node is shared with. Only managers may see this.
func (s *Service) Shares(ctx context.Context, userID, nodeID uuid.UUID) ([]authz.Grant, error) {
	if _, err := s.authorize(ctx, s.db, userID, nodeID, authz.Manage, false); err != nil {
		return nil, err
	}
	return s.sharing.Grants(ctx, nodeType, nodeID)
}

// Share gives a user or group a permission on a node and everything below
// it, or changes the permission of an existing share.
func (s *Service) Share(ctx context.Context, userID, nodeID uuid.UUID, to authz.Subject, p authz.Permission) error {
	if !p.Valid() {
		return &InputError{"Kies kijken, bewerken of beheren."}
	}
	n, err := s.authorize(ctx, s.db, userID, nodeID, authz.Manage, false)
	if err != nil {
		return err
	}
	if root, err := s.isTeamRoot(ctx, s.db, nodeID); err != nil {
		return err
	} else if root {
		return errTeamRoot // its groups are managed by administrators
	}
	if to.Type == authz.User && (to.ID == n.ownerID || to.ID == userID) {
		return &InputError{"Je kunt niet delen met de eigenaar of met jezelf."}
	}
	err = s.sharing.Grant(ctx, userID, nodeType, nodeID, to, p)
	if errors.Is(err, authz.ErrUnknownSubject) {
		return &InputError{"Deze gebruiker of groep bestaat niet (meer)."}
	}
	return err
}

// Unshare removes a share. Managers may remove any share; anyone may
// remove a share with themselves, to drop something shared with them.
func (s *Service) Unshare(ctx context.Context, userID, nodeID uuid.UUID, from authz.Subject) error {
	self := from.Type == authz.User && from.ID == userID
	need := authz.Manage
	if self {
		need = authz.View
	}
	if _, err := s.authorize(ctx, s.db, userID, nodeID, need, false); err != nil {
		return err
	}
	if root, err := s.isTeamRoot(ctx, s.db, nodeID); err != nil {
		return err
	} else if root {
		return errTeamRoot
	}
	err := s.sharing.Revoke(ctx, userID, nodeType, nodeID, from)
	switch {
	case errors.Is(err, authz.ErrNoGrant) && self:
		return &InputError{"Dit is gedeeld met een groep waar je in zit, of met een map erboven; alleen wie het beheert kan dat aanpassen."}
	case errors.Is(err, authz.ErrNoGrant):
		return ErrNotFound
	}
	return err
}

// SharedNode is a node someone else shared with the user.
type SharedNode struct {
	Node
	// OwnerName is the owner's display name, or the team folder's name.
	OwnerName string `json:"owner_name"`
}

// SharedWithMe lists the nodes others shared with the user (directly or
// through a group), skipping what is in the owner's trash. Team folders
// themselves are listed separately (TeamFoldersFor).
func (s *Service) SharedWithMe(ctx context.Context, userID uuid.UUID) ([]SharedNode, error) {
	shares, err := s.sharing.SharedWith(ctx, userID, nodeType)
	if err != nil {
		return nil, fmt.Errorf("list shares: %w", err)
	}
	out := []SharedNode{}
	owners := []uuid.UUID{}
	for _, sh := range shares {
		n, err := s.authorize(ctx, s.db, userID, sh.ResourceID, authz.View, false)
		if errors.Is(err, ErrNotFound) {
			continue // trashed, purged, or the user's own
		}
		if err != nil {
			return nil, err
		}
		if n.ownerID == userID {
			continue // shared with a group the owner is in
		}
		if root, err := s.isTeamRoot(ctx, s.db, n.ID); err != nil {
			return nil, err
		} else if root {
			continue
		}
		n.ParentID = nil // listed on its own, without the owner's folders
		out = append(out, SharedNode{Node: n})
		owners = append(owners, n.ownerID)
	}
	names, err := s.sharing.DisplayNames(ctx, owners)
	if err != nil {
		return nil, fmt.Errorf("load owner names: %w", err)
	}
	for i := range out {
		name, ok := names[out[i].ownerID]
		if !ok {
			team, err := s.teamOf(ctx, out[i].ownerID)
			if err != nil {
				return nil, err
			}
			if team != nil {
				name = "Teammap " + team.Name
			}
		}
		out[i].OwnerName = name
	}
	return out, nil
}
