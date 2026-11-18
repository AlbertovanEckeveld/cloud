package identity

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Permission names one administrative capability (ADR 0022). The set is
// fixed in code: adding one is a code change, never a database change.
type Permission string

const (
	PermUsersView      Permission = "identity.users.view"
	PermUsersInvite    Permission = "identity.users.invite"
	PermUsersEdit      Permission = "identity.users.edit"
	PermUsersDisable   Permission = "identity.users.disable"
	PermUsersReset     Permission = "identity.users.reset"
	PermGroupsView     Permission = "identity.groups.view"
	PermGroupsManage   Permission = "identity.groups.manage"
	PermRolesView      Permission = "identity.roles.view"
	PermRolesManage    Permission = "identity.roles.manage"
	PermRolesAssign    Permission = "identity.roles.assign"
	PermAppsManage     Permission = "identity.apps.manage"
	PermAuditView      Permission = "identity.audit.view"
	PermTeamFolderView Permission = "drive.teamfolders.view"
	PermTeamFolderNew  Permission = "drive.teamfolders.create"
	PermTeamFolderEdit Permission = "drive.teamfolders.manage"
	PermTeamFolderDel  Permission = "drive.teamfolders.delete"
	PermQuotaManage    Permission = "drive.quota.manage"
	PermSystemStatus   Permission = "system.status.view"
)

// PermissionInfo describes a permission for the administration pages.
type PermissionInfo struct {
	Name   Permission `json:"name"`
	Module string     `json:"module"`
	Label  string     `json:"label"`
}

// Catalog lists every permission, in the order the pages show them.
var Catalog = []PermissionInfo{
	{PermUsersView, "identity", "Gebruikers en hun activiteit zien"},
	{PermUsersInvite, "identity", "Gebruikers uitnodigen"},
	{PermUsersEdit, "identity", "Naam en e-mailadres van gebruikers wijzigen"},
	{PermUsersDisable, "identity", "Gebruikers uit- en inschakelen"},
	{PermUsersReset, "identity", "Inloggegevens van gebruikers intrekken (nieuwe activatielink)"},
	{PermGroupsView, "identity", "Groepen en leden zien"},
	{PermGroupsManage, "identity", "Groepen en leden beheren"},
	{PermRolesView, "identity", "Rollen en wie ze heeft zien"},
	{PermRolesManage, "identity", "Rollen maken en wijzigen"},
	{PermRolesAssign, "identity", "Rollen toekennen en intrekken"},
	{PermAppsManage, "identity", "Apps (OIDC) beheren"},
	{PermAuditView, "identity", "De volledige audit-log zien"},
	{PermTeamFolderView, "drive", "Alle teammappen zien (niet de inhoud)"},
	{PermTeamFolderNew, "drive", "Teammappen maken"},
	{PermTeamFolderEdit, "drive", "Teammappen hernoemen, groepen en quotum instellen"},
	{PermTeamFolderDel, "drive", "Teammappen opheffen, terugzetten en definitief verwijderen"},
	{PermQuotaManage, "drive", "Opslaggebruik en quotum per gebruiker beheren"},
	{PermSystemStatus, "system", "Systeemstatus en sites zien"},
}

// AdminRoleID is the built-in role "Beheerder": every permission, also
// ones added later. Its ID is the same on every site.
var AdminRoleID = uuid.MustParse("01a1f100-0000-7000-8000-000000000001")

// KnownPermission reports whether p is in the catalog.
func KnownPermission(p Permission) bool {
	return slices.ContainsFunc(Catalog, func(i PermissionInfo) bool { return i.Name == p })
}

// PermissionSet is what a user may do, as loaded for one request.
type PermissionSet map[Permission]bool

// Has reports whether the set contains p.
func (ps PermissionSet) Has(p Permission) bool { return ps[p] }

// Covers reports whether the set contains every permission in want: the
// rule that nobody gives away more than they have.
func (ps PermissionSet) Covers(want []Permission) bool {
	for _, p := range want {
		if !ps[p] {
			return false
		}
	}
	return true
}

// List returns the permissions in catalog order.
func (ps PermissionSet) List() []Permission {
	out := []Permission{}
	for _, i := range Catalog {
		if ps[i.Name] {
			out = append(out, i.Name)
		}
	}
	return out
}

func allPermissions() []Permission {
	out := make([]Permission, len(Catalog))
	for i, p := range Catalog {
		out[i] = p.Name
	}
	return out
}

// permissionsOf loads a user's effective permissions: the roles given to
// them directly and through their groups. The built-in role means all.
func (s *Service) permissionsOf(ctx context.Context, q dbtx, userID uuid.UUID) (PermissionSet, error) {
	rows, err := q.Query(ctx, `
		SELECT r.builtin, r.permissions FROM identity.roles r
		WHERE r.id IN (
			SELECT role_id FROM identity.role_assignments WHERE user_id = $1
			UNION
			SELECT a.role_id FROM identity.role_assignments a
			JOIN identity.group_members m ON m.group_id = a.group_id WHERE m.user_id = $1)`, userID)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	set := PermissionSet{}
	type role struct {
		builtin bool
		perms   []string
	}
	roles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (role, error) {
		var r role
		return r, row.Scan(&r.builtin, &r.perms)
	})
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	for _, r := range roles {
		if r.builtin {
			for _, p := range allPermissions() {
				set[p] = true
			}
			continue
		}
		for _, p := range r.perms {
			// A name no longer in the catalog grants nothing.
			if KnownPermission(Permission(p)) {
				set[Permission(p)] = true
			}
		}
	}
	return set, nil
}

// Can reports whether the request's user may do p. The desktop app never
// may administer, whatever its user's permissions.
func (p Principal) Can(perm Permission) bool {
	return !p.Device && p.Permissions.Has(perm)
}

// RequirePermission allows a request only for users with perm. It must run
// after RequireUser (or RequireUserOrDevice). An unknown name is a
// programming error and stops the program at start-up.
func RequirePermission(perm Permission) func(http.Handler) http.Handler {
	if !KnownPermission(perm) {
		panic("identity: unknown permission " + string(perm))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := PrincipalFrom(r.Context()); !ok || !p.Can(perm) {
				httpserver.WriteError(w, http.StatusForbidden, "forbidden", "Je hebt hier geen rechten voor.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
