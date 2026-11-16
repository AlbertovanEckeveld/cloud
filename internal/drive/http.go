package drive

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// transferDeadline bounds one upload or download. The server's default
// timeouts (30 s) are far too short for files of gigabytes.
const transferDeadline = 12 * time.Hour

// Auth is what drive needs from the identity module, which owns
// authentication. Keeping it this small keeps the modules decoupled.
type Auth struct {
	RequireUser  func(http.Handler) http.Handler
	RequireAdmin func(http.Handler) http.Handler
	// RequireRecentAuth guards sensitive administrative changes.
	RequireRecentAuth func(http.Handler) http.Handler
	// UserID returns the signed-in user of a request that passed RequireUser.
	UserID func(*http.Request) uuid.UUID
	// Audit records an administrative action in the central audit log.
	Audit func(r *http.Request, event string, details map[string]any)
}

// Routes mounts the drive API.
func (s *Service) Routes(r chi.Router, auth Auth) {
	s.userID, s.audit = auth.UserID, auth.Audit
	// Public share links: no account needed. The token travels in the body,
	// never in the URL, so it does not end up in logs or browser history.
	r.Post("/public/links/open", s.handleOpenLink)
	r.Post("/public/links/download", s.handleLinkDownload)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser)
		r.Get("/folders/{id}", s.handleList)
		r.Post("/folders", s.handleCreateFolder)
		r.Put("/files", s.handleUpload)
		r.Get("/files/{id}/content", s.handleDownload)
		r.Get("/files/{id}/versions", s.handleVersions)
		r.Get("/files/{id}/versions/{version}/content", s.handleDownloadVersion)
		r.Post("/files/{id}/versions/{version}/restore", s.handleRestoreVersion)
		r.Delete("/files/{id}/versions/{version}", s.handleDeleteVersion)
		r.Get("/nodes/{id}/shares", s.handleShares)
		r.Put("/nodes/{id}/shares", s.handleShare)
		r.Delete("/nodes/{id}/shares/{type}/{subject}", s.handleUnshare)
		r.Get("/shared", s.handleSharedWithMe)
		r.Get("/search", s.handleSearch)
		r.Get("/office", s.handleOfficeInfo)
		r.Post("/files/{id}/office", s.handleOpenInOffice)
		r.Get("/nodes/{id}/links", s.handleLinks)
		r.Post("/nodes/{id}/links", s.handleCreateLink)
		r.Delete("/links/{id}", s.handleDeleteLink)
		r.Get("/team-folders", s.handleTeamFoldersFor)
		r.Get("/team-folders/{id}/trash", s.handleTeamTrash)
		r.Delete("/team-folders/{id}/trash", s.handleEmptyTeamTrash)
		r.Patch("/nodes/{id}", s.handleUpdate)
		r.Delete("/nodes/{id}", s.handleTrash)
		r.Get("/trash", s.handleListTrash)
		r.Post("/trash/{id}/restore", s.handleRestore)
		r.Delete("/trash/{id}", s.handleDeleteForever)
		r.Delete("/trash", s.handleEmptyTrash)
		r.Get("/usage", s.handleUsage)
		r.With(auth.RequireAdmin).Get("/admin/users/{id}/usage", s.handleAdminUsage)
		r.With(auth.RequireAdmin).Put("/admin/users/{id}/quota", s.handleAdminQuota)
		r.Route("/admin/team-folders", func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/", s.handleListTeamFolders)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRecentAuth)
				r.Post("/", s.handleCreateTeamFolder)
				r.Patch("/{id}", s.handleRenameTeamFolder)
				r.Put("/{id}/quota", s.handleTeamFolderQuota)
				r.Put("/{id}/groups/{group}", s.handleSetTeamFolderGroup)
				r.Delete("/{id}/groups/{group}", s.handleRemoveTeamFolderGroup)
				r.Delete("/{id}", s.handleDeleteTeamFolder)
				r.Post("/{id}/restore", s.handleRestoreTeamFolder)
				r.Post("/{id}/purge", s.handlePurgeTeamFolder)
			})
		})
	})
}

func pathID(r *http.Request) (uuid.UUID, error) { return pathUUID(r, "id") }

func pathUUID(r *http.Request, param string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// folderParam parses a folder reference: "root" (or empty) is the top level.
func folderParam(s string) (*uuid.UUID, error) {
	if s == "" || s == "root" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, ErrNotFound
	}
	return &id, nil
}

func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if input, ok := errors.AsType[*InputError](err); ok {
		httpserver.WriteError(w, http.StatusUnprocessableEntity, "invalid_input", input.Reason)
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "Niet gevonden.")
	case errors.Is(err, ErrPasswordRequired):
		httpserver.WriteError(w, http.StatusUnauthorized, "password_required", "Deze link is beveiligd met een wachtwoord.")
	case errors.Is(err, ErrWrongPassword):
		httpserver.WriteError(w, http.StatusUnauthorized, "wrong_password", "Het wachtwoord klopt niet.")
	case errors.Is(err, ErrLinkLocked):
		httpserver.WriteError(w, http.StatusTooManyRequests, "locked", "Te veel foute pogingen. Probeer het over een kwartier opnieuw.")
	case errors.Is(err, ErrForbidden):
		httpserver.WriteError(w, http.StatusForbidden, "forbidden", "Je hebt hier niet genoeg rechten voor.")
	case errors.Is(err, ErrNameTaken):
		httpserver.WriteError(w, http.StatusConflict, "name_taken", "Er bestaat al een bestand of map met deze naam.")
	case errors.Is(err, ErrNotFolder):
		httpserver.WriteError(w, http.StatusBadRequest, "not_folder", "Dat is geen map.")
	case errors.Is(err, ErrCycle):
		httpserver.WriteError(w, http.StatusBadRequest, "cycle", "Een map kan niet in zichzelf worden geplaatst.")
	case errors.Is(err, ErrQuotaExceeded):
		httpserver.WriteError(w, http.StatusInsufficientStorage, "quota_exceeded", "Je opslagruimte is vol.")
	case errors.Is(err, ErrTooLarge):
		httpserver.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "Dit bestand is te groot.")
	case errors.Is(err, ErrIncomplete):
		httpserver.WriteError(w, http.StatusBadRequest, "incomplete", "De upload is niet volledig aangekomen.")
	default:
		s.log.Error("drive request failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "Er ging iets mis aan onze kant.")
	}
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	folder, err := folderParam(chi.URLParam(r, "id"))
	var view FolderView
	if err == nil {
		view, err = s.List(r.Context(), s.userID(r), folder)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, view)
}

func (s *Service) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	parent, err := folderParam(in.ParentID)
	var n Node
	if err == nil {
		n, err = s.CreateFolder(r.Context(), s.userID(r), parent, in.Name)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, n)
}

// handleUpload stores the raw request body as a file:
// PUT /files?parent=<id|root>&name=<name>[&replace=true]
func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(transferDeadline)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline)

	if r.ContentLength < 0 {
		httpserver.WriteError(w, http.StatusLengthRequired, "length_required", "De bestandsgrootte ontbreekt.")
		return
	}
	if r.ContentLength > s.maxFile {
		s.writeError(w, r, ErrTooLarge)
		return
	}
	parent, err := folderParam(r.URL.Query().Get("parent"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.Store(r.Context(), s.userID(r), Upload{
		ParentID: parent,
		Name:     r.URL.Query().Get("name"),
		Size:     r.ContentLength,
		Body:     http.MaxBytesReader(w, r.Body, r.ContentLength),
		Replace:  r.URL.Query().Get("replace") == "true",
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, n)
}

// handleDownload serves a file's current content.
func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferDeadline))
	id, err := pathID(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// ?inline=1 shows a previewable file in the page instead of downloading it.
	inline := r.URL.Query().Get("inline") == "1"
	n, obj, err := s.Open(r.Context(), s.userID(r), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer obj.Close()
	var show string
	if inline {
		if show = previewType(n.ContentType); show == "" {
			httpserver.WriteError(w, http.StatusUnsupportedMediaType, "no_preview", "Van dit bestand is geen voorbeeld beschikbaar.")
			return
		}
	}
	serveFile(w, r, n.Name, *n.currentVersionID, n.UpdatedAt, obj, show)
}

func (s *Service) handleDownloadVersion(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferDeadline))
	id, err := pathID(r)
	var version uuid.UUID
	if err == nil {
		version, err = pathUUID(r, "version")
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, v, obj, err := s.OpenVersion(r.Context(), s.userID(r), id, version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer obj.Close()
	serveFile(w, r, n.Name, v.ID, v.CreatedAt, obj, "")
}

// serveFile sends content as an attachment, or, with inlineType (a type
// from previewType), inline for a preview. An uploaded HTML or SVG page must
// never run script on this origin (stored XSS): downloads get a generic
// type, previews only whitelisted ones, and the CSP sandboxes both.
func serveFile(w http.ResponseWriter, r *http.Request, name string, version uuid.UUID, modified time.Time, content io.ReadSeeker, inlineType string) {
	h := w.Header()
	kind := "attachment"
	h.Set("Content-Type", "application/octet-stream")
	if inlineType != "" {
		kind = "inline"
		h.Set("Content-Type", inlineType)
	}
	// FormatMediaType quotes or RFC 2231-encodes the name, so quotes, line
	// breaks or non-ASCII characters cannot break out of the header.
	disposition := mime.FormatMediaType(kind, map[string]string{"filename": name})
	if disposition == "" {
		disposition = kind // never let a failed encoding drop the header
	}
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", `"`+version.String()+`"`)
	// ServeContent handles Range and conditional requests (resumable downloads).
	http.ServeContent(w, r, "", modified, content)
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name *string `json:"name"`
		// ParentID moves the node: "root" for the top level, or a folder ID.
		ParentID *string `json:"parent_id"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	ch := Change{Name: in.Name}
	if err == nil && in.ParentID != nil {
		ch.Parent, err = folderParam(*in.ParentID)
		ch.ToTop = ch.Parent == nil
	}
	var n Node
	if err == nil {
		n, err = s.Update(r.Context(), s.userID(r), id, ch)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, n)
}

func (s *Service) handleTrash(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.Trash(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListTrash(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.ListTrash(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Nodes []Node `json:"nodes"`
	}{nodes})
}

func (s *Service) handleRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var n Node
	if err == nil {
		n, err = s.Restore(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, n)
}

func (s *Service) handleDeleteForever(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeleteForever(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	if err := s.EmptyTrash(r.Context(), s.userID(r)); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUsage(w http.ResponseWriter, r *http.Request) {
	u, err := s.Usage(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, u)
}

func (s *Service) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var u Usage
	if err == nil {
		u, err = s.Usage(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, u)
}

func (s *Service) handleAdminQuota(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LimitBytes *int64 `json:"limit_bytes"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		err = s.SetQuota(r.Context(), id, in.LimitBytes)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleAdminUsage(w, r)
}

func (s *Service) handleVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var versions []Version
	if err == nil {
		versions, err = s.Versions(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Versions []Version `json:"versions"`
	}{versions})
}

func (s *Service) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var version uuid.UUID
	if err == nil {
		version, err = pathUUID(r, "version")
	}
	var n Node
	if err == nil {
		n, err = s.RestoreVersion(r.Context(), s.userID(r), id, version)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, n)
}

func (s *Service) handleDeleteVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var version uuid.UUID
	if err == nil {
		version, err = pathUUID(r, "version")
	}
	if err == nil {
		err = s.DeleteVersion(r.Context(), s.userID(r), id, version)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleShares(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var grants []authz.Grant
	if err == nil {
		grants, err = s.Shares(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Shares []authz.Grant `json:"shares"`
	}{grants})
}

// handleShare creates or changes a share: {"subject": {"type", "id"}, "permission"}.
func (s *Service) handleShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject    authz.Subject    `json:"subject"`
		Permission authz.Permission `json:"permission"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil && in.Subject.Type != authz.User && in.Subject.Type != authz.Group {
		err = &InputError{"Kies een gebruiker of groep."}
	}
	if err == nil {
		err = s.Share(r.Context(), s.userID(r), id, in.Subject, in.Permission)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleShares(w, r)
}

func (s *Service) handleUnshare(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	from := authz.Subject{Type: authz.SubjectType(chi.URLParam(r, "type"))}
	if err == nil {
		from.ID, err = pathUUID(r, "subject")
	}
	if err == nil && from.Type != authz.User && from.Type != authz.Group {
		err = ErrNotFound
	}
	if err == nil {
		err = s.Unshare(r.Context(), s.userID(r), id, from)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSharedWithMe(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.SharedWithMe(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Nodes []SharedNode `json:"nodes"`
	}{nodes})
}

func (s *Service) handleTeamFoldersFor(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.TeamFoldersFor(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Nodes []Node `json:"nodes"`
	}{nodes})
}

func (s *Service) handleTeamTrash(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var team TeamRef
	var nodes []Node
	if err == nil {
		team, nodes, err = s.ListTeamTrash(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Team  TeamRef `json:"team"`
		Nodes []Node  `json:"nodes"`
	}{team, nodes})
}

func (s *Service) handleEmptyTeamTrash(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.EmptyTeamTrash(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListTeamFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.ListTeamFolders(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		TeamFolders []TeamFolder `json:"team_folders"`
	}{folders})
}

// teamFolderDone audits a successful administrative change and answers with
// the team folder's current state.
func (s *Service) teamFolderDone(w http.ResponseWriter, r *http.Request, id uuid.UUID, event string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["team_folder_id"] = id.String()
	s.audit(r, event, details)
	t, err := s.TeamFolder(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, t)
}

func (s *Service) handleCreateTeamFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	t, err := s.CreateTeamFolder(r.Context(), s.userID(r), in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "teamfolder.created", map[string]any{"team_folder_id": t.ID.String(), "name": t.Name})
	httpserver.WriteJSON(w, http.StatusCreated, t)
}

func (s *Service) handleRenameTeamFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		err = s.RenameTeamFolder(r.Context(), id, in.Name)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.teamFolderDone(w, r, id, "teamfolder.renamed", map[string]any{"name": in.Name})
}

func (s *Service) handleTeamFolderQuota(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LimitBytes *int64 `json:"limit_bytes"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	if err == nil {
		err = s.SetTeamFolderQuota(r.Context(), id, in.LimitBytes)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.teamFolderDone(w, r, id, "teamfolder.quota_changed", map[string]any{"limit_bytes": in.LimitBytes})
}

func (s *Service) handleSetTeamFolderGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Permission authz.Permission `json:"permission"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	var group uuid.UUID
	if err == nil {
		group, err = pathUUID(r, "group")
	}
	if err == nil {
		// The share itself is audited by the identity module.
		err = s.SetTeamFolderGroup(r.Context(), s.userID(r), id, group, in.Permission)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleTeamFolder(w, r, id)
}

func (s *Service) handleRemoveTeamFolderGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var group uuid.UUID
	if err == nil {
		group, err = pathUUID(r, "group")
	}
	if err == nil {
		err = s.RemoveTeamFolderGroup(r.Context(), s.userID(r), id, group)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.handleTeamFolder(w, r, id)
}

func (s *Service) handleTeamFolder(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	t, err := s.TeamFolder(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, t)
}

func (s *Service) handleDeleteTeamFolder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeleteTeamFolder(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.teamFolderDone(w, r, id, "teamfolder.deleted", nil)
}

func (s *Service) handleRestoreTeamFolder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.RestoreTeamFolder(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.teamFolderDone(w, r, id, "teamfolder.restored", nil)
}

func (s *Service) handlePurgeTeamFolder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.PurgeTeamFolder(r.Context(), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "teamfolder.purged", map[string]any{"team_folder_id": id.String()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleLinks(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var links []Link
	if err == nil {
		links, err = s.Links(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Links []Link `json:"links"`
	}{links})
}

func (s *Service) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpiresAt *time.Time `json:"expires_at"`
		Password  string     `json:"password"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	id, err := pathID(r)
	var l Link
	if err == nil {
		l, err = s.CreateLink(r.Context(), s.userID(r), id, in.ExpiresAt, in.Password)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "link.created", map[string]any{"link_id": l.ID.String(), "node_id": id.String(), "password": l.Password})
	httpserver.WriteJSON(w, http.StatusCreated, l)
}

func (s *Service) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		err = s.DeleteLink(r.Context(), s.userID(r), id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "link.deleted", map[string]any{"link_id": id.String()})
	w.WriteHeader(http.StatusNoContent)
}

// handleOpenLink: {"token", "password", "folder_id"} -> PublicView.
func (s *Service) handleOpenLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		FolderID string `json:"folder_id"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	var folder *uuid.UUID
	if in.FolderID != "" {
		id, err := uuid.Parse(in.FolderID)
		if err != nil {
			s.writeError(w, r, ErrNotFound)
			return
		}
		folder = &id
	}
	view, err := s.OpenLink(r.Context(), in.Token, in.Password, folder)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, view)
}

// handleLinkDownload serves a file of a link. It takes a small form post
// (token, password, file_id), so a plain browser download works without the
// token appearing in a URL.
func (s *Service) handleLinkDownload(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferDeadline))
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_input", "Ongeldig verzoek.")
		return
	}
	file, err := uuid.Parse(r.PostForm.Get("file_id"))
	if err != nil {
		s.writeError(w, r, ErrNotFound)
		return
	}
	n, obj, err := s.OpenLinkFile(r.Context(), r.PostForm.Get("token"), r.PostForm.Get("password"), file)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer obj.Close()
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	serveFile(w, r, n.Name, *n.currentVersionID, n.UpdatedAt, obj, "")
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		q = q[:200]
	}
	results, err := s.Search(r.Context(), s.userID(r), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Results []SearchResult `json:"results"`
	}{results})
}
