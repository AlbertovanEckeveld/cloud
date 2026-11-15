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
	// UserID returns the signed-in user of a request that passed RequireUser.
	UserID func(*http.Request) uuid.UUID
}

// Routes mounts the drive API.
func (s *Service) Routes(r chi.Router, auth Auth) {
	s.userID = auth.UserID
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
		r.Patch("/nodes/{id}", s.handleUpdate)
		r.Delete("/nodes/{id}", s.handleTrash)
		r.Get("/trash", s.handleListTrash)
		r.Post("/trash/{id}/restore", s.handleRestore)
		r.Delete("/trash/{id}", s.handleDeleteForever)
		r.Delete("/trash", s.handleEmptyTrash)
		r.Get("/usage", s.handleUsage)
		r.With(auth.RequireAdmin).Get("/admin/users/{id}/usage", s.handleAdminUsage)
		r.With(auth.RequireAdmin).Put("/admin/users/{id}/quota", s.handleAdminQuota)
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
	n, obj, err := s.Open(r.Context(), s.userID(r), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer obj.Close()
	serveFile(w, r, n.Name, *n.currentVersionID, n.UpdatedAt, obj)
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
	serveFile(w, r, n.Name, v.ID, v.CreatedAt, obj)
}

// serveFile sends content as an attachment. Files are never rendered by the
// browser: an uploaded HTML or SVG page must not run script on this origin
// (stored XSS), so the type is generic and the CSP sandboxes it too.
func serveFile(w http.ResponseWriter, r *http.Request, name string, version uuid.UUID, modified time.Time, content io.ReadSeeker) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	// FormatMediaType quotes or RFC 2231-encodes the name, so quotes, line
	// breaks or non-ASCII characters cannot break out of the header.
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if disposition == "" {
		disposition = "attachment" // never let a failed encoding drop the header
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
