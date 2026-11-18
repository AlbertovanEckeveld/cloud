package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// TokenSource hands out a current access token (the app refreshes it).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Errors the engine reacts to.
var (
	ErrCursorExpired = errors.New("sync cursor expired")
	ErrUnauthorized  = errors.New("device not (or no longer) signed in")
)

// Client talks to the Cloud sync and file API.
type Client struct {
	base   *url.URL
	http   *http.Client
	tokens TokenSource
}

// NewClient returns a client for server, which must use https unless
// allowInsecure is set (a development server reached over an encrypted
// network). Redirects are never followed: a token must not end up elsewhere.
func NewClient(server string, allowInsecure bool, tokens TokenSource) (*Client, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, fmt.Errorf("invalid server address %q", server)
	}
	if u.Scheme != "https" && !allowInsecure {
		return nil, fmt.Errorf("server %s does not use https", u.Host)
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return &Client{
		base:   u,
		tokens: tokens,
		http: &http.Client{
			Timeout:       0, // transfers can take long; requests use contexts
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// RemoteItem is a node as the sync API reports it.
type RemoteItem struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Kind     string  `json:"kind"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	SHA256   string  `json:"sha256"`
	Gone     bool    `json:"gone"`
}

// Page is a batch of the tree or of changes.
type Page struct {
	Cursor string       `json:"cursor"`
	Items  []RemoteItem `json:"items"`
	More   bool         `json:"more"`
}

// Root is a folder tree that can be synchronised.
type Root struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Permission string `json:"permission"`
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, size int64, contentType string) (*http.Response, error) {
	u := *c.base
	u.Path = path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		req.Header.Set("Content-Type", contentType)
	}
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case http.StatusGone:
			return nil, ErrCursorExpired
		}
		return nil, fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil, 0, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Bounded: a listing of a very large tree is still far below this.
	return json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(out)
}

// Roots lists what the user can synchronise.
func (c *Client) Roots(ctx context.Context) ([]Root, error) {
	var out struct{ Roots []Root }
	err := c.getJSON(ctx, "/api/v1/drive/sync/roots", nil, &out)
	return out.Roots, err
}

// Tree returns the whole tree of a root and the cursor for changes.
func (c *Client) Tree(ctx context.Context, root string) (Page, error) {
	var p Page
	err := c.getJSON(ctx, "/api/v1/drive/sync/tree", url.Values{"root": {root}}, &p)
	return p, err
}

// Changes returns what changed since cursor.
func (c *Client) Changes(ctx context.Context, root, cursor string) (Page, error) {
	var p Page
	err := c.getJSON(ctx, "/api/v1/drive/sync/changes", url.Values{"root": {root}, "cursor": {cursor}}, &p)
	return p, err
}

// Download streams a file's current content into w, at most limit bytes.
func (c *Client) Download(ctx context.Context, id string, w io.Writer, limit int64) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/v1/drive/files/"+url.PathEscape(id)+"/content", nil, nil, 0, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		return fmt.Errorf("download of %s is larger than announced", id)
	}
	return err
}

type nodeJSON struct {
	ID string `json:"id"`
}

// Upload stores a file in parent ("root" for the own top level).
func (c *Client) Upload(ctx context.Context, parent, name string, size int64, body io.Reader) (string, error) {
	q := url.Values{"parent": {parent}, "name": {name}, "replace": {"true"}}
	resp, err := c.do(ctx, http.MethodPut, "/api/v1/drive/files", q, body, size, "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var n nodeJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&n); err != nil {
		return "", err
	}
	return n.ID, nil
}

// Mkdir creates a folder in parent and returns its ID.
func (c *Client) Mkdir(ctx context.Context, parent, name string) (string, error) {
	b, _ := json.Marshal(map[string]string{"parent_id": parent, "name": name})
	resp, err := c.do(ctx, http.MethodPost, "/api/v1/drive/folders", nil, bytes.NewReader(b), int64(len(b)), "application/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var n nodeJSON
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&n); err != nil {
		return "", err
	}
	return n.ID, nil
}

// Trash moves a node to the trash (recoverable for 30 days).
func (c *Client) Trash(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/v1/drive/nodes/"+url.PathEscape(id), nil, nil, 0, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ReportDevice tells the server this computer's name and platform, shown in
// the user's list of linked devices.
func (c *Client) ReportDevice(ctx context.Context, name, platform string) error {
	return c.putJSON(ctx, "/api/v1/device", map[string]string{"name": name, "platform": platform})
}

// ReportSync tells the server which roots this computer keeps in sync, after
// a round over all of them.
func (c *Client) ReportSync(ctx context.Context, roots []string) error {
	return c.putJSON(ctx, "/api/v1/drive/sync/device", map[string][]string{"roots": roots})
}

func (c *Client) putJSON(ctx context.Context, path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, path, nil, bytes.NewReader(b), int64(len(b)), "application/json")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
