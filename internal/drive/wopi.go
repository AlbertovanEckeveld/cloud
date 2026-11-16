package drive

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// WOPI (Web Application Open Platform Interface) lets Collabora Online edit
// documents stored in drive (ADR 0015). The browser loads the editor from
// Collabora; Collabora fetches and saves the document here, server to
// server, with an access token the platform issued for one user and file.

const (
	wopiTokenTTL     = 8 * time.Hour
	discoveryRefresh = time.Hour
)

// Office configures editing with Collabora Online.
type Office struct {
	// URL is the Collabora server (discovery is read from it).
	URL *url.URL
	// PublicURL is this platform's address, as Collabora reaches it.
	PublicURL *url.URL
	// Key signs access tokens.
	Key [32]byte
}

// office holds the editor URLs from Collabora's discovery document.
type office struct {
	cfg Office

	mu       sync.Mutex
	fetched  time.Time
	actions  map[string]officeAction // by lower-case file extension
	proof    *rsa.PublicKey          // nil when Collabora publishes none
	proofOld *rsa.PublicKey
}

type officeAction struct {
	edit, view string // urlsrc without placeholders; edit empty if view-only
}

type discoveryXML struct {
	Apps []struct {
		Actions []struct {
			Name   string `xml:"name,attr"`
			Ext    string `xml:"ext,attr"`
			URLSrc string `xml:"urlsrc,attr"`
		} `xml:"action"`
	} `xml:"net-zone>app"`
	ProofKey struct {
		Modulus     string `xml:"modulus,attr"`
		Exponent    string `xml:"exponent,attr"`
		OldModulus  string `xml:"oldmodulus,attr"`
		OldExponent string `xml:"oldexponent,attr"`
	} `xml:"proof-key"`
}

// load reads the discovery document, at most once per discoveryRefresh.
func (o *office) load(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.actions != nil && time.Since(o.fetched) < discoveryRefresh {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.URL.String()+"/hosting/discovery", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("collabora discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("collabora discovery: status %d", resp.StatusCode)
	}
	var d discoveryXML
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return fmt.Errorf("collabora discovery: %w", err)
	}
	actions := map[string]officeAction{}
	for _, app := range d.Apps {
		for _, a := range app.Actions {
			if a.Ext == "" {
				continue
			}
			src, err := url.Parse(a.URLSrc)
			// The editor must live where the CSP allows frames: on the
			// configured Collabora origin and nowhere else.
			if err != nil || src.Scheme != o.cfg.URL.Scheme || src.Host != o.cfg.URL.Host {
				return fmt.Errorf("collabora discovery: editor URL %q is not on %s; set Collabora's server_name to match COLLABORA_URL", a.URLSrc, o.cfg.URL)
			}
			// Drop the <ui=UI_LLCC&>-style placeholders; we add our own parameters.
			clean := strings.Split(a.URLSrc, "<")[0]
			act := actions[strings.ToLower(a.Ext)]
			switch a.Name {
			case "edit":
				act.edit = clean
			case "view", "embedview":
				if act.view == "" {
					act.view = clean
				}
			}
			actions[strings.ToLower(a.Ext)] = act
		}
	}
	o.proof = rsaKey(d.ProofKey.Modulus, d.ProofKey.Exponent)
	o.proofOld = rsaKey(d.ProofKey.OldModulus, d.ProofKey.OldExponent)
	o.actions, o.fetched = actions, time.Now()
	return nil
}

func rsaKey(modulus, exponent string) *rsa.PublicKey {
	m, err1 := base64.StdEncoding.DecodeString(modulus)
	e, err2 := base64.StdEncoding.DecodeString(exponent)
	if modulus == "" || err1 != nil || err2 != nil || len(e) > 8 {
		return nil
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(m), E: int(new(big.Int).SetBytes(e).Int64())}
}

// action returns the editor URL for a file name: the edit action when
// edit is set and available, else the view action.
func (o *office) action(ctx context.Context, name string, edit bool) (string, bool, error) {
	if err := o.load(ctx); err != nil {
		return "", false, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	a, ok := o.actions[strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")]
	switch {
	case !ok:
		return "", false, nil
	case edit && a.edit != "":
		return a.edit, true, nil
	case a.view != "":
		return a.view, false, nil
	case a.edit != "":
		return a.edit, false, nil // edit action in read-only mode (UserCanWrite false)
	}
	return "", false, nil
}

// Extensions lists the file extensions Collabora can open.
func (o *office) extensions(ctx context.Context) ([]string, error) {
	if err := o.load(ctx); err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.actions))
	for ext := range o.actions {
		out = append(out, ext)
	}
	return out, nil
}

// wopiToken is what an access token grants: one user, one file, until exp.
type wopiToken struct {
	User  uuid.UUID `json:"u"`
	File  uuid.UUID `json:"f"`
	Write bool      `json:"w"`
	Exp   int64     `json:"e"` // Unix milliseconds
}

func (o *office) sign(t wopiToken) string {
	payload, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, o.cfg.Key[:])
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (o *office) verify(token string, now time.Time) (wopiToken, bool) {
	p, m, ok := strings.Cut(token, ".")
	if !ok {
		return wopiToken{}, false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	sum, err2 := base64.RawURLEncoding.DecodeString(m)
	if err1 != nil || err2 != nil {
		return wopiToken{}, false
	}
	mac := hmac.New(sha256.New, o.cfg.Key[:])
	mac.Write(payload)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return wopiToken{}, false
	}
	var t wopiToken
	if json.Unmarshal(payload, &t) != nil || now.UnixMilli() >= t.Exp {
		return wopiToken{}, false
	}
	return t, true
}

// verifyProof checks Collabora's signature on a WOPI request (X-WOPI-Proof),
// when Collabora publishes a proof key. The signed data is: token length,
// token, URL length, upper-case URL, 8, timestamp (all lengths 4-byte
// big-endian). Requests older than 20 minutes are refused.
func (o *office) verifyProof(r *http.Request, token string) bool {
	o.mu.Lock()
	key, old := o.proof, o.proofOld
	o.mu.Unlock()
	if key == nil {
		return true // nothing to verify against; the access token still applies
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-WOPI-TimeStamp"), 10, 64)
	if err != nil {
		return false
	}
	// .NET ticks (100 ns since 0001-01-01) to Unix time.
	sent := time.Unix(0, (ts-621355968000000000)*100)
	if time.Since(sent) > 20*time.Minute {
		return false
	}
	full := strings.ToUpper(o.cfg.PublicURL.String() + r.URL.RequestURI())
	var b []byte
	b = binary.BigEndian.AppendUint32(b, uint32(len(token)))
	b = append(b, token...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(full)))
	b = append(b, full...)
	b = binary.BigEndian.AppendUint32(b, 8)
	b = binary.BigEndian.AppendUint64(b, uint64(ts))
	digest := sha256.Sum256(b)
	check := func(k *rsa.PublicKey, header string) bool {
		sig, err := base64.StdEncoding.DecodeString(r.Header.Get(header))
		return k != nil && err == nil && rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) == nil
	}
	return check(key, "X-WOPI-Proof") || check(key, "X-WOPI-ProofOld") || check(old, "X-WOPI-Proof")
}

// OfficeCheck reports whether Collabora is reachable and configured to match
// COLLABORA_URL, for the system status page. The reason for a failure is
// logged by the health checker.
func (s *Service) OfficeCheck(ctx context.Context) error {
	return s.office.load(ctx)
}

// OfficeSession is what the browser needs to open the editor.
type OfficeSession struct {
	URL         string `json:"url"`
	AccessToken string `json:"access_token"`
	TTL         int64  `json:"access_token_ttl"` // expiry, Unix milliseconds (WOPI)
	Edit        bool   `json:"edit"`
}

// OpenInOffice issues an editor session for a file the user may view;
// editing when they may edit and Collabora can edit the type.
func (s *Service) OpenInOffice(ctx context.Context, userID, fileID uuid.UUID) (OfficeSession, error) {
	if s.office == nil {
		return OfficeSession{}, ErrNoOffice
	}
	n, err := s.file(ctx, s.db, userID, fileID, authz.View)
	if err != nil {
		return OfficeSession{}, err
	}
	src, edit, err := s.office.action(ctx, n.Name, n.Permission.Allows(authz.Edit))
	if err != nil {
		return OfficeSession{}, err
	}
	if src == "" {
		return OfficeSession{}, ErrNoOffice
	}
	exp := s.now().Add(wopiTokenTTL).UnixMilli()
	tok := s.office.sign(wopiToken{User: userID, File: fileID, Write: edit, Exp: exp})
	q := url.Values{
		"WOPISrc": {s.office.cfg.PublicURL.String() + "/wopi/files/" + fileID.String()},
		"lang":    {"nl"},
	}
	sep := "?"
	if strings.Contains(src, "?") {
		sep = "&"
		if strings.HasSuffix(src, "?") || strings.HasSuffix(src, "&") {
			sep = ""
		}
	}
	return OfficeSession{URL: src + sep + q.Encode(), AccessToken: tok, TTL: exp, Edit: edit}, nil
}

// ErrNoOffice means documents cannot be opened in the editor (not
// configured, or not a type Collabora handles).
var ErrNoOffice = errors.New("no office editor for this file")

// WOPIRoutes mounts the WOPI host endpoints Collabora calls.
func (s *Service) WOPIRoutes(r chi.Router) {
	r.Get("/files/{id}", s.wopi(s.wopiCheckFileInfo))
	r.Get("/files/{id}/contents", s.wopi(s.wopiGetFile))
	r.Post("/files/{id}/contents", s.wopi(s.wopiPutFile))
	// Locks, renames and "save as" are not offered (CheckFileInfo says so).
	r.Post("/files/{id}", s.wopi(func(w http.ResponseWriter, _ *http.Request, _ wopiToken) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
}

type wopiHandler func(http.ResponseWriter, *http.Request, wopiToken)

// wopi checks the access token (and Collabora's proof signature) before a
// WOPI call. Failures are 401 or 404 without detail (WOPI spec).
func (s *Service) wopi(h wopiHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.office == nil {
			http.NotFound(w, r)
			return
		}
		token := r.URL.Query().Get("access_token")
		t, ok := s.office.verify(token, s.now())
		if !ok || !s.office.verifyProof(r, token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if id, err := uuid.Parse(chi.URLParam(r, "id")); err != nil || id != t.File {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h(w, r, t)
	}
}

func wopiTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000Z") }

func (s *Service) wopiCheckFileInfo(w http.ResponseWriter, r *http.Request, t wopiToken) {
	n, err := s.file(r.Context(), s.db, t.User, t.File, authz.View)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	names, _ := s.sharing.DisplayNames(r.Context(), []uuid.UUID{t.User})
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"BaseFileName":            n.Name,
		"OwnerId":                 n.ownerID.String(),
		"UserId":                  t.User.String(),
		"UserFriendlyName":        names[t.User],
		"Size":                    n.Size,
		"Version":                 n.currentVersionID.String(),
		"LastModifiedTime":        wopiTime(n.UpdatedAt),
		"UserCanWrite":            t.Write && n.Permission.Allows(authz.Edit),
		"SupportsUpdate":          true,
		"SupportsLocks":           false,
		"SupportsRename":          false,
		"UserCanRename":           false,
		"UserCanNotWriteRelative": true,
		"PostMessageOrigin":       s.office.cfg.PublicURL.String(),
		"DisableExport":           false,
	})
}

func (s *Service) wopiGetFile(w http.ResponseWriter, r *http.Request, t wopiToken) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferDeadline))
	n, obj, err := s.Open(r.Context(), t.User, t.File)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer obj.Close()
	w.Header().Set("X-WOPI-ItemVersion", n.currentVersionID.String())
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", n.UpdatedAt, obj)
}

// wopiPutFile saves the document as a new version. When the file changed
// since the editor loaded it (another upload, WebDAV, a second editor),
// Collabora's timestamp no longer matches and the save is refused with
// COOLStatusCode 1010, so Collabora can ask the user what to do.
func (s *Service) wopiPutFile(w http.ResponseWriter, r *http.Request, t wopiToken) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(transferDeadline))
	if !t.Write {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	n, err := s.file(r.Context(), s.db, t.User, t.File, authz.Edit)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if ts := r.Header.Get("X-COOL-WOPI-Timestamp"); ts != "" && ts != wopiTime(n.UpdatedAt) {
		httpserver.WriteJSON(w, http.StatusConflict, map[string]int{"COOLStatusCode": 1010})
		return
	}
	size := r.ContentLength
	if size < 0 {
		size = -1
	}
	saved, err := s.Store(r.Context(), t.User, Upload{FileID: &t.File, Size: size, Body: r.Body})
	switch {
	case errors.Is(err, ErrQuotaExceeded), errors.Is(err, ErrTooLarge):
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		s.log.Warn("wopi save failed", "file", t.File, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-WOPI-ItemVersion", saved.currentVersionID.String())
	httpserver.WriteJSON(w, http.StatusOK, map[string]string{"LastModifiedTime": wopiTime(saved.UpdatedAt)})
}

func (s *Service) handleOpenInOffice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var sess OfficeSession
	if err == nil {
		sess, err = s.OpenInOffice(r.Context(), s.userID(r), id)
	}
	if errors.Is(err, ErrNoOffice) {
		httpserver.WriteError(w, http.StatusUnsupportedMediaType, "no_office", "Dit bestand kan niet in de editor worden geopend.")
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, sess)
}

// handleOfficeInfo tells the frontend whether editing is available and for
// which extensions.
func (s *Service) handleOfficeInfo(w http.ResponseWriter, r *http.Request) {
	out := struct {
		Enabled    bool     `json:"enabled"`
		Origin     string   `json:"origin"`
		Extensions []string `json:"extensions"`
	}{Extensions: []string{}}
	if s.office != nil {
		exts, err := s.office.extensions(r.Context())
		if err != nil {
			s.log.Warn("collabora unavailable", "error", err)
		} else {
			out.Enabled, out.Origin, out.Extensions = true, s.office.cfg.URL.String(), exts
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}
