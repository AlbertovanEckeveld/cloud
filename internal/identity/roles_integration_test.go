//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// newRole creates a role (as an administrator) that is removed afterwards.
func (e *testEnv) newRole(t *testing.T, admin Session, perms ...Permission) Role {
	t.Helper()
	r, err := e.svc.CreateRole(context.Background(), e.client, e.as(t, admin),
		RoleInput{Name: fmt.Sprintf("it-role-%d-%d", time.Now().UnixNano()%1e9, testSeq.Add(1)), Permissions: perms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := e.pool.Exec(context.Background(), `DELETE FROM identity.roles WHERE id = $1`, r.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return r
}

// helpdesk is a user with a role of exactly these permissions, signed in.
func (e *testEnv) helpdesk(t *testing.T, admin Session, perms ...Permission) (User, Principal) {
	t.Helper()
	u := e.newUser(t)
	_, token := e.activate(t, u)
	role := e.newRole(t, admin, perms...)
	if err := e.svc.SetRole(context.Background(), e.client, e.as(t, admin), role.ID, RoleSubject{User: &u.ID}, true); err != nil {
		t.Fatal(err)
	}
	return u, e.as(t, e.session(t, token))
}

func TestPermissionsFromRolesAndGroups(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	_, admin := e.newAdmin(t)
	u := e.newUser(t)
	role := e.newRole(t, admin, PermTeamFolderNew, PermTeamFolderView)
	g := e.newGroup(t, admin)

	perms := func() []Permission {
		ps, err := e.svc.permissionsOf(ctx, e.pool, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ps.List()
	}
	if len(perms()) != 0 {
		t.Fatalf("new user has permissions: %v", perms())
	}
	// Through a group: as long as the user is a member.
	if err := e.svc.SetRole(ctx, e.client, e.as(t, admin), role.ID, RoleSubject{Group: &g.ID}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, admin), g.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got := perms(); !slices.Equal(got, []Permission{PermTeamFolderView, PermTeamFolderNew}) {
		t.Fatalf("through group: %v", got)
	}
	if roles, _ := e.svc.UserRoles(ctx, u.ID); len(roles) != 1 || roles[0].Group == nil || *roles[0].Group != g.Name {
		t.Fatalf("user roles: %+v", roles)
	}
	if err := e.svc.SetGroupMember(ctx, e.client, e.as(t, admin), g.ID, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(perms()) != 0 {
		t.Fatalf("after leaving the group: %v", perms())
	}
	// The built-in role is everything, also what the catalog adds later.
	ps, _ := e.svc.permissionsOf(ctx, e.pool, admin.UserID)
	if !slices.Equal(ps.List(), allPermissions()) {
		t.Fatalf("administrator: %v", ps.List())
	}
}

func TestPermissionChecksOnRoutes(t *testing.T) {
	o := newOIDCEnv(t)
	_, admin := o.newAdmin(t)
	u, _ := o.helpdesk(t, admin, PermUsersView)
	o.signIn(t, u)

	get := func(path string) int {
		resp, err := o.browser.Get(o.base + "/api/v1" + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := get("/admin/users"); st != http.StatusOK {
		t.Errorf("users with identity.users.view: %d", st)
	}
	for _, path := range []string{"/admin/groups", "/admin/roles", "/admin/audit", "/admin/apps"} {
		if st := get(path); st != http.StatusForbidden {
			t.Errorf("%s without its permission: %d", path, st)
		}
	}
	// The session tells the pages what the user may do.
	resp, err := o.browser.Get(o.base + "/api/v1/auth/session")
	if err != nil {
		t.Fatal(err)
	}
	var sess struct {
		User struct{ Permissions []Permission }
	}
	_ = json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()
	if !slices.Equal(sess.User.Permissions, []Permission{PermUsersView}) {
		t.Errorf("session permissions: %v", sess.User.Permissions)
	}
	defer func() {
		if recover() == nil {
			t.Error("an unknown permission name did not stop the program")
		}
	}()
	RequirePermission("identity.users.everything")
}

func TestNoEscalation(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	admin, adminSess := e.newAdmin(t)
	plain := e.newUser(t)
	_, desk := e.helpdesk(t, adminSess,
		PermUsersView, PermUsersEdit, PermUsersReset, PermUsersDisable,
		PermGroupsManage, PermRolesView, PermRolesManage, PermRolesAssign)

	escalates := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrEscalation) {
			t.Errorf("%s: err = %v, want ErrEscalation", name, err)
		}
	}

	// 1. No role with a permission you lack, no assigning one.
	_, err := e.svc.CreateRole(ctx, e.client, desk, RoleInput{Name: "it-never", Permissions: []Permission{PermAuditView}})
	escalates("create role with a permission one lacks", err)
	escalates("give the built-in role", e.svc.SetRole(ctx, e.client, desk, AdminRoleID, RoleSubject{User: &plain.ID}, true))
	escalates("take the built-in role", e.svc.SetRole(ctx, e.client, desk, AdminRoleID, RoleSubject{User: &admin.ID}, false))
	small := e.newRole(t, adminSess, PermUsersView)
	if err := e.svc.SetRole(ctx, e.client, desk, small.ID, RoleSubject{User: &plain.ID}, true); err != nil {
		t.Errorf("give a role within one's own permissions: %v", err)
	}

	// 2. No widening a role beyond what you have.
	escalates("widen a role", e.svc.UpdateRole(ctx, e.client, desk, small.ID,
		RoleInput{Name: small.Name, Permissions: []Permission{PermUsersView, PermAppsManage}}))

	// 3. Joining a group with a role is being given that role.
	admins := e.newGroup(t, adminSess)
	if err := e.svc.SetRole(ctx, e.client, e.as(t, adminSess), AdminRoleID, RoleSubject{Group: &admins.ID}, true); err != nil {
		t.Fatal(err)
	}
	escalates("join an administrators' group", e.svc.SetGroupMember(ctx, e.client, desk, admins.ID, desk.Session.UserID, true))
	escalates("delete an administrators' group", e.svc.DeleteGroup(ctx, e.client, desk, admins.ID))
	plainGroup := e.newGroup(t, adminSess)
	if err := e.svc.SetGroupMember(ctx, e.client, desk, plainGroup.ID, plain.ID, true); err != nil {
		t.Errorf("membership of a group without roles: %v", err)
	}

	// 4. No reaching into stronger accounts.
	_, err = e.svc.ResetUser(ctx, e.client, desk, admin.ID)
	escalates("reset an administrator", err)
	escalates("re-address an administrator", e.svc.UpdateUser(ctx, e.client, desk, admin.ID, UserUpdate{Email: new("evil@example.test")}))
	escalates("disable an administrator", e.svc.SetUserDisabled(ctx, e.client, desk, admin.ID, true))
	if _, err := e.svc.ResetUser(ctx, e.client, desk, plain.ID); err != nil {
		t.Errorf("reset an equal account: %v", err)
	}

	// The built-in role cannot be changed or deleted, not even by an administrator.
	var input *InputError
	if err := e.svc.UpdateRole(ctx, e.client, e.as(t, adminSess), AdminRoleID, RoleInput{Name: "Iets anders"}); !errors.As(err, &input) {
		t.Errorf("change the built-in role: %v", err)
	}
	if err := e.svc.DeleteRole(ctx, e.client, e.as(t, adminSess), AdminRoleID); !errors.As(err, &input) {
		t.Errorf("delete the built-in role: %v", err)
	}
	_, err = e.svc.CreateRole(ctx, e.client, e.as(t, adminSess), RoleInput{Name: "it-unknown", Permissions: []Permission{"drive.everything"}})
	if !errors.As(err, &input) {
		t.Errorf("unknown permission: %v", err)
	}

	// Every change is in the audit log.
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM identity.audit_events WHERE actor_id = $1 AND event = $2`,
		desk.Session.UserID, EventRoleAssigned).Scan(&n)
	if n != 1 {
		t.Errorf("role assignments audited: %d", n)
	}
}

// TestLastAdministratorGuard checks the rule every change runs before it
// commits: some enabled user must still hold the built-in role, directly or
// through a group. It works in a transaction that is rolled back, so other
// administrators in the test database neither hide the case nor change.
func TestLastAdministratorGuard(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	admin, sess := e.newAdmin(t)
	g := e.newGroup(t, sess)

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Only this administrator, and only through the group.
	exec(`DELETE FROM identity.role_assignments WHERE role_id = $1`, AdminRoleID)
	exec(`INSERT INTO identity.role_assignments (role_id, group_id, created_at) VALUES ($1, $2, now())`, AdminRoleID, g.ID)
	exec(`INSERT INTO identity.group_members (group_id, user_id, added_at) VALUES ($1, $2, now())`, g.ID, admin.ID)
	if err := ensureAdminRemains(ctx, tx); err != nil {
		t.Fatalf("administrator through a group not counted: %v", err)
	}
	exec(`UPDATE identity.users SET disabled_at = now() WHERE id = $1`, admin.ID)
	if err := ensureAdminRemains(ctx, tx); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("a disabled administrator counted: %v", err)
	}
	exec(`UPDATE identity.users SET disabled_at = NULL WHERE id = $1`, admin.ID)
	exec(`DELETE FROM identity.group_members WHERE group_id = $1`, g.ID)
	if err := ensureAdminRemains(ctx, tx); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("no administrator left, but no error: %v", err)
	}
}
