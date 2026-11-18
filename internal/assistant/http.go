package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Auth is what the assistant needs from identity.
type Auth struct {
	// RequireUser allows signed-in browser sessions (not devices).
	RequireUser       func(http.Handler) http.Handler
	RequirePermission func(permission string) func(http.Handler) http.Handler
	RequireRecentAuth func(http.Handler) http.Handler
	UserID            func(*http.Request) uuid.UUID
	Audit             func(r *http.Request, event string, details map[string]any)
}

// Routes mounts the assistant API.
func (s *Service) Routes(r chi.Router, auth Auth) {
	s.userID, s.audit = auth.UserID, auth.Audit
	r.Use(auth.RequireUser)
	r.Get("/status", s.handleStatus)
	r.Get("/search", s.handleSearch)
	r.Get("/conversations", s.handleConversations)
	r.Post("/conversations", s.handleCreateConversation)
	r.Get("/conversations/{id}", s.handleConversation)
	r.Delete("/conversations/{id}", s.handleDeleteConversation)
	r.Post("/conversations/{id}/messages", s.handleAsk)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequirePermission("assistant.provider.manage"))
		r.Get("/providers", s.handleProviders)
		r.Get("/settings", s.handleGetSettings)
		r.Get("/index", s.handleIndex)
		r.Post("/providers/{id}/test", s.handleTestProvider)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRecentAuth)
			r.Post("/providers", s.handleSaveProvider(false))
			r.Patch("/providers/{id}", s.handleSaveProvider(true))
			r.Delete("/providers/{id}", s.handleDeleteProvider)
			r.Put("/settings", s.handleSaveSettings)
		})
	})
}

func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var input *InputError
	var provider *ProviderError
	switch {
	case errors.As(err, &input):
		httpserver.WriteError(w, http.StatusUnprocessableEntity, "invalid_input", input.Reason)
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "Niet gevonden.")
	case errors.Is(err, ErrNotConfigured):
		httpserver.WriteError(w, http.StatusConflict, "not_configured", "De assistent is nog niet ingesteld.")
	case errors.As(err, &provider):
		s.log.Warn("assistant provider failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", logDetail(err))
		httpserver.WriteError(w, http.StatusBadGateway, "provider_"+provider.Kind, provider.Error())
	default:
		s.log.Error("assistant request failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "Er ging iets mis.")
	}
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// status tells the pages what is available, without provider details.
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.GetSettings(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := struct {
		Search       bool   `json:"search"`
		Chat         bool   `json:"chat"`
		ExternalChat string `json:"external_chat"` // host the chat goes to, if outside the home network
	}{Search: st.EmbedProvider != nil, Chat: st.ChatProvider != nil}
	if st.ChatProvider != nil {
		if _, p, err := s.clientFor(r.Context(), s.db, *st.ChatProvider); err == nil && p.External {
			if u, err := parseBaseURL(p.BaseURL); err == nil {
				out.ExternalChat = u.Hostname()
			}
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	results, err := s.Search(r.Context(), s.userID(r), r.URL.Query().Get("q"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Service) handleConversations(w http.ResponseWriter, r *http.Request) {
	list, err := s.Conversations(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

func (s *Service) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	c, err := s.CreateConversation(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, c)
}

func (s *Service) handleConversation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var c ConversationDetail
	if err == nil {
		c, err = s.GetConversation(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, c)
}

func (s *Service) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeleteConversation(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAsk streams the answer as server-sent events: "delta" with a piece
// of text, then "done" with the stored message, or "error".
func (s *Service) handleAsk(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(11 * time.Minute))
	started := false
	send := func(event string, data any) {
		if !started {
			h := w.Header()
			h.Set("Content-Type", "text/event-stream")
			h.Set("Cache-Control", "no-store")
			h.Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		_ = rc.Flush()
	}
	msg, err := s.Ask(r.Context(), s.userID(r), id, in.Content, func(text string) {
		send("delta", map[string]string{"text": text})
	})
	if err != nil {
		if !started {
			s.writeError(w, r, err) // nothing streamed yet: a normal error response
			return
		}
		var provider *ProviderError
		reason := "Er ging iets mis."
		if errors.As(err, &provider) {
			s.log.Warn("assistant provider failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", logDetail(err))
			reason = provider.Error()
		} else {
			s.log.Error("assistant answer failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
		}
		send("error", map[string]string{"message": reason})
		return
	}
	send("done", msg)
}

func (s *Service) handleProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.Providers(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"providers": list})
}

func (s *Service) handleSaveProvider(update bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var id *uuid.UUID
		if update {
			pid, err := pathID(r)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			id = &pid
		}
		var in struct {
			Name            string  `json:"name"`
			BaseURL         string  `json:"base_url"`
			APIKey          *string `json:"api_key"` // absent: keep; "": remove
			ConsentExternal bool    `json:"consent_external"`
		}
		if !httpserver.DecodeJSON(w, r, &in) {
			return
		}
		p, err := s.SaveProvider(r.Context(), s.userID(r), id, ProviderInput{
			Name: in.Name, BaseURL: in.BaseURL, Key: in.APIKey, ConsentExternal: in.ConsentExternal})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		details := map[string]any{"provider": p.Name, "host": hostOf(p.BaseURL), "external": p.External}
		if in.APIKey != nil {
			details["key_changed"] = true // the key itself never goes to the log
		}
		s.audit(r, "assistant.provider.saved", details)
		status := http.StatusOK
		if !update {
			status = http.StatusCreated
		}
		httpserver.WriteJSON(w, status, p)
	}
}

func hostOf(raw string) string {
	if u, err := parseBaseURL(raw); err == nil {
		return u.Host
	}
	return ""
}

func (s *Service) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeleteProvider(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "assistant.provider.deleted", map[string]any{"provider": id.String()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var models []string
	if err == nil {
		models, err = s.TestProvider(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.GetSettings(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	if err := s.SaveSettings(r.Context(), in); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "assistant.settings.saved", map[string]any{
		"chat_model": strings.TrimSpace(in.ChatModel), "embed_model": strings.TrimSpace(in.EmbedModel)})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleIndex(w http.ResponseWriter, r *http.Request) {
	st, err := s.Status(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, st)
}
