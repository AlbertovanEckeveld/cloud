package identity

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// adminRoutes mounts account, group, role and app administration. Every
// route requires its own permission (ADR 0022); changes also require a
// recent sign-in, checked by the service.
func (s *Service) adminRoutes(r chi.Router) {
	r.Use(s.RequireUser)
	can := func(p Permission) chi.Router { return r.With(RequirePermission(p)) }
	can(PermUsersView).Get("/users", s.handleListUsers)
	can(PermUsersInvite).Post("/users", s.handleInviteUser)
	can(PermUsersView).Get("/users/{id}", s.handleGetUser)
	can(PermUsersEdit).Patch("/users/{id}", s.handleUpdateUser)
	can(PermUsersView).Get("/users/{id}/activity", s.handleUserActivity)
	can(PermUsersReset).Post("/users/{id}/reset", s.handleResetUser)
	can(PermUsersDisable).Post("/users/{id}/disable", s.handleSetDisabled(true))
	can(PermUsersDisable).Post("/users/{id}/enable", s.handleSetDisabled(false))
	can(PermGroupsView).Get("/groups", s.handleListGroups)
	can(PermGroupsManage).Post("/groups", s.handleCreateGroup)
	can(PermGroupsView).Get("/groups/{id}", s.handleGetGroup)
	can(PermGroupsManage).Patch("/groups/{id}", s.handleUpdateGroup)
	can(PermGroupsManage).Delete("/groups/{id}", s.handleDeleteGroup)
	can(PermGroupsManage).Put("/groups/{id}/members/{userID}", s.handleSetMember(true))
	can(PermGroupsManage).Delete("/groups/{id}/members/{userID}", s.handleSetMember(false))
	can(PermRolesView).Get("/permissions", s.handlePermissionCatalog)
	can(PermRolesView).Get("/roles", s.handleListRoles)
	can(PermRolesManage).Post("/roles", s.handleCreateRole)
	can(PermRolesManage).Patch("/roles/{id}", s.handleUpdateRole)
	can(PermRolesManage).Delete("/roles/{id}", s.handleDeleteRole)
	can(PermRolesAssign).Put("/roles/{id}/users/{subject}", s.handleSetRole(false, true))
	can(PermRolesAssign).Delete("/roles/{id}/users/{subject}", s.handleSetRole(false, false))
	can(PermRolesAssign).Put("/roles/{id}/groups/{subject}", s.handleSetRole(true, true))
	can(PermRolesAssign).Delete("/roles/{id}/groups/{subject}", s.handleSetRole(true, false))
	can(PermAuditView).Get("/audit", s.handleListAudit)
	can(PermAppsManage).Get("/apps", s.handleListClients)
	can(PermAppsManage).Post("/apps", s.handleCreateClient)
	can(PermAppsManage).Get("/apps/{clientID}", s.handleGetClient)
	can(PermAppsManage).Patch("/apps/{clientID}", s.handleUpdateClient)
	can(PermAppsManage).Post("/apps/{clientID}/secret", s.handleRotateClientSecret)
	can(PermAppsManage).Delete("/apps/{clientID}", s.handleDeleteClient)
}

func (s *Service) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.ListUsers(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Users []AdminUser `json:"users"`
	}{users})
}

func (s *Service) handleInviteUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	u, a, err := s.InviteUser(r.Context(), clientFrom(r), p.Session,
		NewUser{Username: in.Username, DisplayName: in.DisplayName, Email: in.Email})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, struct {
		User User `json:"user"`
		Activation
	}{u, a})
}

func (s *Service) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var u AdminUser
	if err == nil {
		u, err = s.GetUser(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, u)
}

func (s *Service) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.UpdateUser(r.Context(), clientFrom(r), p, id, UserUpdate{DisplayName: in.DisplayName, Email: in.Email})
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUserActivity(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var events []AuditEvent
	if err == nil {
		events, err = s.Activity(r.Context(), id, nil, 50)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Events []AuditEvent `json:"events"`
	}{events})
}

func (s *Service) handleResetUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var a Activation
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		a, err = s.ResetUser(r.Context(), clientFrom(r), p, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, a)
}

func (s *Service) handleSetDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		if err == nil {
			p, _ := PrincipalFrom(r.Context())
			err = s.SetUserDisabled(r.Context(), clientFrom(r), p, id, disabled)
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Service) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.ListGroups(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Groups []Group `json:"groups"`
	}{groups})
}

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Service) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var in groupRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	g, err := s.CreateGroup(r.Context(), clientFrom(r), p.Session, GroupInput(in))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, g)
}

func (s *Service) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var g GroupDetail
	if err == nil {
		g, err = s.GetGroup(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, g)
}

func (s *Service) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var in groupRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.UpdateGroup(r.Context(), clientFrom(r), p.Session, id, GroupInput(in))
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.DeleteGroup(r.Context(), clientFrom(r), p, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSetMember(member bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupID, err := pathID(r)
		userID, userErr := uuid.Parse(chi.URLParam(r, "userID"))
		if err == nil && userErr != nil {
			err = ErrNotFound
		}
		if err == nil {
			p, _ := PrincipalFrom(r.Context())
			err = s.SetGroupMember(r.Context(), clientFrom(r), p, groupID, userID, member)
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

const auditPageSize = 50

func (s *Service) handleListAudit(w http.ResponseWriter, r *http.Request) {
	var before *uuid.UUID
	if raw := r.URL.Query().Get("before"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "Het verzoek is ongeldig.")
			return
		}
		before = &id
	}
	limit := auditPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			httpserver.WriteError(w, http.StatusBadRequest, "invalid_request", "Het verzoek is ongeldig.")
			return
		}
		limit = n
	}
	entries, err := s.ListAudit(r.Context(), before, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Entries []AuditEntry `json:"entries"`
	}{entries})
}

type clientRequest struct {
	Name                   string     `json:"name"`
	Confidential           bool       `json:"confidential"`
	RedirectURIs           []string   `json:"redirect_uris"`
	PostLogoutRedirectURIs []string   `json:"post_logout_redirect_uris"`
	AllowedGroupID         *uuid.UUID `json:"allowed_group_id"`
	BackchannelLogoutURI   string     `json:"backchannel_logout_uri"`
}

func (in clientRequest) input() OIDCClientInput {
	return OIDCClientInput{Name: in.Name, Confidential: in.Confidential, RedirectURIs: in.RedirectURIs,
		PostLogoutRedirectURIs: in.PostLogoutRedirectURIs, AllowedGroupID: in.AllowedGroupID, BackchannelLogoutURI: in.BackchannelLogoutURI}
}

// clientSecretResponse returns a client with its secret, which is shown once.
type clientSecretResponse struct {
	OIDCClient
	Secret string `json:"secret,omitempty"`
}

func (s *Service) handleListClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.ListOIDCClients(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Issuer  string       `json:"issuer"`
		Clients []OIDCClient `json:"clients"`
	}{s.oidcIssuer(), clients})
}

func (s *Service) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var in clientRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	c, secret, err := s.CreateOIDCClient(r.Context(), clientFrom(r), p.Session, in.input())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, clientSecretResponse{c, secret})
}

func (s *Service) handleGetClient(w http.ResponseWriter, r *http.Request) {
	c, err := s.GetOIDCClient(r.Context(), chi.URLParam(r, "clientID"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, c)
}

func (s *Service) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	var in clientRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := s.UpdateOIDCClient(r.Context(), clientFrom(r), p.Session, chi.URLParam(r, "clientID"), in.input()); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRotateClientSecret(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	secret, err := s.RotateOIDCClientSecret(r.Context(), clientFrom(r), p.Session, chi.URLParam(r, "clientID"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Secret string `json:"secret"`
	}{secret})
}

func (s *Service) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	if err := s.DeleteOIDCClient(r.Context(), clientFrom(r), p.Session, chi.URLParam(r, "clientID")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handlePermissionCatalog(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Permissions []PermissionInfo `json:"permissions"`
	}{Catalog})
}

func (s *Service) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.ListRoles(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Roles []Role `json:"roles"`
	}{roles})
}

type roleBody struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
}

func (s *Service) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var in roleBody
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	role, err := s.CreateRole(r.Context(), clientFrom(r), p, RoleInput(in))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, role)
}

func (s *Service) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	var in roleBody
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.UpdateRole(r.Context(), clientFrom(r), p, id, RoleInput(in))
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.DeleteRole(r.Context(), clientFrom(r), p, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetRole gives (on) or takes a role from a user or, with group, a
// group: /roles/{id}/users/{subject} and /roles/{id}/groups/{subject}.
func (s *Service) handleSetRole(group, on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := pathID(r)
		var subject uuid.UUID
		if err == nil {
			subject, err = uuid.Parse(chi.URLParam(r, "subject"))
			if err != nil {
				err = ErrNotFound
			}
		}
		if err == nil {
			to := RoleSubject{User: &subject}
			if group {
				to = RoleSubject{Group: &subject}
			}
			p, _ := PrincipalFrom(r.Context())
			err = s.SetRole(r.Context(), clientFrom(r), p, id, to, on)
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
