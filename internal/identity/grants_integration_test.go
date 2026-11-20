//go:build integration

package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

const testResource = "test.thing"

func TestGrantsDirectAndThroughGroups(t *testing.T) {
	e := newTestEnv(t)
	owner, actor := e.newAdmin(t)
	alice, bob := e.newUser(t), e.newUser(t)
	g := e.newGroup(t, actor)
	ctx := context.Background()
	if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, actor), g.ID, bob.ID, true); err != nil {
		t.Fatal(err)
	}
	doc, folder := uuid.New(), uuid.New()
	t.Cleanup(func() { _ = e.svc.RevokeAll(context.Background(), testResource, []uuid.UUID{doc, folder}) })

	if err := e.svc.Grant(ctx, owner.ID, testResource, doc, authz.Subject{Type: authz.User, ID: alice.ID}, authz.View); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Grant(ctx, owner.ID, testResource, folder, authz.Subject{Type: authz.Group, ID: g.ID}, authz.Edit); err != nil {
		t.Fatal(err)
	}
	// Changing a share updates it instead of adding a second one.
	if err := e.svc.Grant(ctx, owner.ID, testResource, doc, authz.Subject{Type: authz.User, ID: alice.ID}, authz.Manage); err != nil {
		t.Fatal(err)
	}

	perms, err := e.svc.Permissions(ctx, alice.ID, testResource, []uuid.UUID{doc, folder})
	if err != nil || perms[doc] != authz.Manage || perms[folder] != authz.None {
		t.Fatalf("alice = %v, %v", perms, err)
	}
	perms, _ = e.svc.Permissions(ctx, bob.ID, testResource, []uuid.UUID{doc, folder})
	if perms[folder] != authz.Edit || len(perms) != 1 {
		t.Fatalf("bob (through group) = %v", perms)
	}

	grants, err := e.svc.Grants(ctx, testResource, doc)
	if err != nil || len(grants) != 1 || grants[0].Name != "Test User" || grants[0].Permission != authz.Manage {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	if shared, _ := e.svc.Shared(ctx, testResource, []uuid.UUID{doc, uuid.New()}); !shared[doc] || len(shared) != 1 {
		t.Errorf("shared = %v", shared)
	}
	if names, _ := e.svc.DisplayNames(ctx, []uuid.UUID{alice.ID, uuid.New()}); len(names) != 1 || names[alice.ID] != "Test User" {
		t.Errorf("display names = %v", names)
	}
	if list, _ := e.svc.SharedWith(ctx, bob.ID, testResource); len(list) != 1 || list[0].ResourceID != folder || list[0].GrantedBy != owner.ID {
		t.Errorf("shared with bob = %+v", list)
	}

	// Leaving the group ends access immediately.
	if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, actor), g.ID, bob.ID, false); err != nil {
		t.Fatal(err)
	}
	if perms, _ := e.svc.Permissions(ctx, bob.ID, testResource, []uuid.UUID{folder}); len(perms) != 0 {
		t.Errorf("after leaving the group: %v", perms)
	}
	// A disabled user holds nothing.
	if err := e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if perms, _ := e.svc.Permissions(ctx, alice.ID, testResource, []uuid.UUID{doc}); len(perms) != 0 {
		t.Errorf("disabled user still has %v", perms)
	}

	// Deleting the group removes its shares.
	if err := e.svc.DeleteGroup(ctx, e.client, e.as(t, actor), g.ID); err != nil {
		t.Fatal(err)
	}
	if grants, _ := e.svc.Grants(ctx, testResource, folder); len(grants) != 0 {
		t.Errorf("group share survived the group: %+v", grants)
	}

	if err := e.svc.Revoke(ctx, owner.ID, testResource, doc, authz.Subject{Type: authz.User, ID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Revoke(ctx, owner.ID, testResource, doc, authz.Subject{Type: authz.User, ID: alice.ID}); !errors.Is(err, authz.ErrNoGrant) {
		t.Errorf("second revoke: %v", err)
	}
	var audited int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM identity.audit_events WHERE actor_id = $1 AND event IN ($2, $3)`,
		owner.ID, EventShareGranted, EventShareRevoked).Scan(&audited)
	if audited != 4 {
		t.Errorf("share audit events = %d, want 4", audited)
	}
}

func TestGrantRejectsBadTargets(t *testing.T) {
	e := newTestEnv(t)
	owner, actor := e.newAdmin(t)
	off := e.newUser(t)
	ctx := context.Background()
	if err := e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), off.ID, true); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	for name, tc := range map[string]struct {
		to   authz.Subject
		p    authz.Permission
		want error
	}{
		"unknown user":  {authz.Subject{Type: authz.User, ID: uuid.New()}, authz.View, authz.ErrUnknownSubject},
		"disabled user": {authz.Subject{Type: authz.User, ID: off.ID}, authz.View, authz.ErrUnknownSubject},
		"unknown group": {authz.Subject{Type: authz.Group, ID: uuid.New()}, authz.View, authz.ErrUnknownSubject},
		"bad level":     {authz.Subject{Type: authz.User, ID: owner.ID}, "admin", authz.ErrInvalidPermission},
	} {
		if err := e.svc.Grant(ctx, owner.ID, testResource, id, tc.to, tc.p); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

func TestDirectory(t *testing.T) {
	e := newTestEnv(t)
	_, actor := e.newAdmin(t)
	u := e.newUser(t)
	g := e.newGroup(t, actor)
	ctx := context.Background()

	entries, err := e.svc.Directory(ctx, u.Username)
	if err != nil || len(entries) != 1 || entries[0].Type != authz.User || entries[0].ID != u.ID {
		t.Fatalf("by username: %+v, %v", entries, err)
	}
	if entries, _ := e.svc.Directory(ctx, g.Name); len(entries) != 1 || entries[0].Type != authz.Group {
		t.Errorf("by group name: %+v", entries)
	}
	// LIKE wildcards in the query are taken literally.
	if entries, _ := e.svc.Directory(ctx, "%_%"); len(entries) != 0 {
		t.Errorf("wildcard query matched %d entries", len(entries))
	}
	if err := e.svc.SetUserDisabled(ctx, e.client, e.as(t, actor), u.ID, true); err != nil {
		t.Fatal(err)
	}
	if entries, _ := e.svc.Directory(ctx, u.Username); len(entries) != 0 {
		t.Errorf("disabled user listed: %+v", entries)
	}
}
