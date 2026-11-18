package identity

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// adminRoutes mounts account and group administration. Every route requires
// an administrator; changes also require a recent sign-in.
func (s *Service) adminRoutes(r chi.Router) {
	r.Use(s.RequireUser, RequireAdmin)
	r.Get("/users", s.handleListUsers)
	r.Post("/users", s.handleInviteUser)
	r.Get("/users/{id}", s.handleGetUser)
	r.Patch("/users/{id}", s.handleUpdateUser)
	r.Get("/users/{id}/activity", s.handleUserActivity)
	r.Post("/users/{id}/reset", s.handleResetUser)
	r.Post("/users/{id}/disable", s.handleSetDisabled(true))
	r.Post("/users/{id}/enable", s.handleSetDisabled(false))
	r.Get("/groups", s.handleListGroups)
	r.Post("/groups", s.handleCreateGroup)
	r.Get("/groups/{id}", s.handleGetGroup)
	r.Patch("/groups/{id}", s.handleUpdateGroup)
	r.Delete("/groups/{id}", s.handleDeleteGroup)
	r.Put("/groups/{id}/members/{userID}", s.handleSetMember(true))
	r.Delete("/groups/{id}/members/{userID}", s.handleSetMember(false))
	r.Get("/audit", s.handleListAudit)
	r.Get("/apps", s.handleListClients)
	r.Post("/apps", s.handleCreateClient)
	r.Get("/apps/{clientID}", s.handleGetClient)
	r.Patch("/apps/{clientID}", s.handleUpdateClient)
	r.Post("/apps/{clientID}/secret", s.handleRotateClientSecret)
	r.Delete("/apps/{clientID}", s.handleDeleteClient)
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
		IsAdmin     bool   `json:"is_admin"`
		Email       string `json:"email"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	u, a, err := s.InviteUser(r.Context(), clientFrom(r), p.Session,
		NewUser{Username: in.Username, DisplayName: in.DisplayName, IsAdmin: in.IsAdmin, Email: in.Email})
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
		IsAdmin     *bool   `json:"is_admin"`
		Email       *string `json:"email"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.UpdateUser(r.Context(), clientFrom(r), p.Session, id, UserUpdate{DisplayName: in.DisplayName, IsAdmin: in.IsAdmin, Email: in.Email})
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
		a, err = s.ResetUser(r.Context(), clientFrom(r), p.Session, id)
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
			err = s.SetUserDisabled(r.Context(), clientFrom(r), p.Session, id, disabled)
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
		err = s.DeleteGroup(r.Context(), clientFrom(r), p.Session, id)
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
			err = s.SetGroupMember(r.Context(), clientFrom(r), p.Session, groupID, userID, member)
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
