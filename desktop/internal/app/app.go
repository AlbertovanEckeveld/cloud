package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/auth"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/syncer"
)

const (
	pollEvery = 30 * time.Second // remote changes
	settle    = 2 * time.Second  // wait for a burst of local changes to end
)

// FolderStatus is what the window shows per synced folder.
type FolderStatus struct {
	Folder
	LastRun    time.Time      `json:"last_run"`
	Report     syncer.Report  `json:"report"`
	Error      string         `json:"error"`
	MassDelete *MassDeleteAsk `json:"mass_delete"`
}

// MassDeleteAsk is a run the mass-delete brake stopped.
type MassDeleteAsk struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
	Total  int `json:"total"`
}

// Status is the app's state for the window.
type Status struct {
	Server    string         `json:"server"`
	SignedIn  bool           `json:"signed_in"`
	Syncing   bool           `json:"syncing"`
	Paused    bool           `json:"paused"`
	Autostart bool           `json:"autostart"`
	Folders   []FolderStatus `json:"folders"`
	Error     string         `json:"error"`
}

// App is the running desktop app.
type App struct {
	log  *slog.Logger
	host string
	emit func(Status) // tells the window about a new status

	mu        sync.Mutex
	cfg       Config
	sess      *auth.Session
	state     *syncer.State
	status    map[string]*FolderStatus
	confirmed map[string]bool
	syncing   bool
	paused    bool
	lastErr   string
	trigger   chan struct{}
	stop      context.CancelFunc
}

// New loads the settings and resumes a previous sign-in.
func New(log *slog.Logger, emit func(Status)) (*App, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	state, err := syncer.OpenState(filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	a := &App{log: log, host: host, emit: emit, cfg: cfg, state: state,
		status: map[string]*FolderStatus{}, confirmed: map[string]bool{}, trigger: make(chan struct{}, 1)}
	if cfg.Server != "" {
		if s, err := auth.Restore(context.Background(), cfg.Server, cfg.AllowInsecure); err == nil {
			a.sess = s
		} else if !errors.Is(err, auth.ErrSignedOut) {
			a.lastErr = "De server is niet bereikbaar; later opnieuw."
		}
	}
	return a, nil
}

// Start runs the sync loop until ctx ends.
func (a *App) Start(ctx context.Context) {
	ctx, a.stop = context.WithCancel(ctx)
	go a.loop(ctx)
	go a.watch(ctx)
	a.Kick()
}

// Close stops syncing and closes the state.
func (a *App) Close() {
	if a.stop != nil {
		a.stop()
	}
	a.state.Close()
}

// Kick asks for a sync run soon.
func (a *App) Kick() {
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

func (a *App) loop(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.trigger:
		}
		a.runAll(ctx)
	}
}

func (a *App) runAll(ctx context.Context) {
	a.mu.Lock()
	sess, cfg := a.sess, a.cfg
	if sess == nil || len(cfg.Folders) == 0 || a.paused {
		a.mu.Unlock()
		return
	}
	a.syncing = true
	a.mu.Unlock()
	a.publish()

	client, err := syncer.NewClient(cfg.Server, cfg.AllowInsecure, sess)
	if err == nil {
		engine := syncer.NewEngine(client, a.state, a.host, a.log)
		for _, f := range cfg.Folders {
			a.mu.Lock()
			confirmed := a.confirmed[f.Root]
			delete(a.confirmed, f.Root)
			a.mu.Unlock()
			rep, err := engine.Sync(ctx, f.Root, f.Dir, confirmed)
			a.record(f, rep, err)
			if errors.Is(err, syncer.ErrUnauthorized) || errors.Is(err, auth.ErrSignedOut) {
				a.mu.Lock()
				a.sess = nil
				a.lastErr = "Dit apparaat is afgemeld. Log opnieuw in."
				a.mu.Unlock()
				break
			}
		}
	}
	a.mu.Lock()
	a.syncing = false
	a.mu.Unlock()
	a.publish()
}

func (a *App) record(f Folder, rep syncer.Report, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := &FolderStatus{Folder: f, LastRun: time.Now(), Report: rep}
	var mass *syncer.MassDeleteError
	switch {
	case errors.As(err, &mass):
		st.MassDelete = &MassDeleteAsk{Local: mass.Local, Remote: mass.Remote, Total: mass.Total}
	case err != nil:
		a.log.Warn("sync failed", "folder", f.Dir, "error", err)
		st.Error = "Synchroniseren mislukt; wordt opnieuw geprobeerd."
	case len(rep.Failed) > 0:
		st.Error = fmt.Sprintf("%d item(s) konden niet worden gesynchroniseerd; wordt opnieuw geprobeerd.", len(rep.Failed))
	}
	a.status[f.Root] = st
}

// watch follows changes in the synced folders and starts a run once a burst
// of changes has settled.
func (a *App) watch(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		a.log.Warn("file watching unavailable; syncing every 30 seconds", "error", err)
		return
	}
	defer w.Close()
	watched := map[string]bool{}
	add := func(dir string) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if !watched[p] && w.Add(p) == nil {
				watched[p] = true
			}
			return nil
		})
	}
	refresh := func() {
		a.mu.Lock()
		folders := a.cfg.Folders
		a.mu.Unlock()
		for _, f := range folders {
			add(f.Dir)
		}
	}
	refresh()
	var timer *time.Timer
	every := time.NewTicker(time.Minute) // pick up newly added sync folders
	defer every.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-every.C:
			refresh()
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if strings.HasPrefix(filepath.Base(ev.Name), ".cloud-tmp-") {
				continue // our own downloads
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() {
					add(ev.Name)
				}
			}
			if timer == nil {
				timer = time.AfterFunc(settle, a.Kick)
			} else {
				timer.Reset(settle)
			}
		case <-w.Errors:
		}
	}
}

func (a *App) publish() {
	if a.emit != nil {
		a.emit(a.Status())
	}
}

// Status returns the current state.
func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Status{Server: a.cfg.Server, SignedIn: a.sess != nil, Syncing: a.syncing, Paused: a.paused, Autostart: a.cfg.Autostart, Error: a.lastErr, Folders: []FolderStatus{}}
	for _, f := range a.cfg.Folders {
		if st, ok := a.status[f.Root]; ok {
			s.Folders = append(s.Folders, *st)
		} else {
			s.Folders = append(s.Folders, FolderStatus{Folder: f})
		}
	}
	return s
}

// SignIn signs in to server through the browser.
func (a *App) SignIn(ctx context.Context, server string, allowInsecure bool, openBrowser func(string)) error {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	sess, err := auth.SignIn(ctx, server, allowInsecure, openBrowser)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.cfg.Server != server {
		a.cfg.Folders = nil // folders belong to the server they came from
	}
	a.cfg.Server, a.cfg.AllowInsecure, a.sess, a.lastErr = server, allowInsecure, sess, ""
	cfg := a.cfg
	a.mu.Unlock()
	if err := saveConfig(cfg); err != nil {
		return err
	}
	a.publish()
	return nil
}

// SignOut revokes this device's sign-in. Local files stay where they are.
func (a *App) SignOut(ctx context.Context) error {
	a.mu.Lock()
	sess := a.sess
	a.sess = nil
	a.mu.Unlock()
	if sess != nil {
		if err := sess.SignOut(ctx); err != nil {
			return err
		}
	}
	a.publish()
	return nil
}

// Roots lists what the server offers for syncing.
func (a *App) Roots(ctx context.Context) ([]syncer.Root, error) {
	a.mu.Lock()
	sess, cfg := a.sess, a.cfg
	a.mu.Unlock()
	if sess == nil {
		return nil, auth.ErrSignedOut
	}
	client, err := syncer.NewClient(cfg.Server, cfg.AllowInsecure, sess)
	if err != nil {
		return nil, err
	}
	return client.Roots(ctx)
}

// AddFolder links a server root to a local folder.
func (a *App) AddFolder(root, name, dir string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.cfg.Folders {
		if f.Root == root {
			return errors.New("deze map wordt al gesynchroniseerd")
		}
	}
	clean, err := checkFolder(dir, a.cfg.Folders)
	if err != nil {
		return err
	}
	a.cfg.Folders = append(a.cfg.Folders, Folder{Root: root, Name: name, Dir: clean})
	if err := saveConfig(a.cfg); err != nil {
		return err
	}
	go a.Kick()
	return nil
}

// RemoveFolder stops syncing a folder; its files stay on disk and server.
func (a *App) RemoveFolder(root string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := a.cfg.Folders[:0]
	for _, f := range a.cfg.Folders {
		if f.Root != root {
			kept = append(kept, f)
		}
	}
	a.cfg.Folders = kept
	delete(a.status, root)
	return saveConfig(a.cfg)
}

// SetPaused stops or resumes syncing (until the app restarts).
func (a *App) SetPaused(on bool) {
	a.mu.Lock()
	a.paused = on
	a.mu.Unlock()
	a.publish()
	if !on {
		a.Kick()
	}
}

// OpenFolder shows a synced folder in the file manager. Only folders the
// app syncs are opened, never an arbitrary path.
func (a *App) OpenFolder(dir string) error {
	for _, f := range a.Status().Folders {
		if f.Dir == dir {
			return openURL((&url.URL{Scheme: "file", Path: dir}).String())
		}
	}
	return errors.New("deze map wordt niet gesynchroniseerd")
}

// ConfirmMassDelete lets the next run of a folder delete what the brake held back.
func (a *App) ConfirmMassDelete(root string) {
	a.mu.Lock()
	a.confirmed[root] = true
	a.mu.Unlock()
	a.Kick()
}

// SetAutostart starts the app at login, or not.
func (a *App) SetAutostart(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := setAutostart(on, exe); err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Autostart = on
	cfg := a.cfg
	a.mu.Unlock()
	return saveConfig(cfg)
}
