// Package app runs the desktop app: configuration, the sync loop with file
// watching, and the functions the window calls.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Folder links a server root to a local folder.
type Folder struct {
	Root string `json:"root"` // "mine", or a team folder's or share's ID
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// Config is the app's settings. It holds no secrets: the sign-in is in the
// keyring.
type Config struct {
	Server        string   `json:"server"`
	AllowInsecure bool     `json:"allow_insecure"`
	Folders       []Folder `json:"folders"`
	Autostart     bool     `json:"autostart"`
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "cloud-desktop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.Chmod(dir, 0o700)
}

// SocketPath is where the service listens for the settings window: in
// $XDG_RUNTIME_DIR when the desktop provides it (a private 0700 tmpfs per
// user, the standard place for sockets on Linux), else in the private
// config folder.
func SocketPath() (string, error) {
	if run := os.Getenv("XDG_RUNTIME_DIR"); run != "" {
		if info, err := os.Stat(run); err == nil && info.IsDir() && info.Mode().Perm()&0o077 == 0 {
			return filepath.Join(run, "cloud-desktop.sock"), nil
		}
	}
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "service.sock"), nil
}

func loadConfig() (Config, error) {
	dir, err := configDir()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("read settings: %w", err)
	}
	return c, nil
}

func saveConfig(c Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

// checkFolder refuses folders that would make syncing dangerous: not
// absolute, missing, a symlink, the whole home or system, or overlapping
// another synced folder (which would sync into itself).
func checkFolder(dir string, others []Folder) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("kies een volledige map")
	}
	clean := filepath.Clean(dir)
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("deze map bestaat niet (of is een snelkoppeling)")
	}
	home, _ := os.UserHomeDir()
	cfg, _ := configDir()
	if clean == "/" || clean == home || within(clean, cfg) || within(cfg, clean) {
		return "", errors.New("kies een eigen map, niet je hele thuismap of een systeemmap")
	}
	for _, f := range others {
		if within(clean, f.Dir) || within(f.Dir, clean) {
			return "", fmt.Errorf("deze map overlapt met %q, die al wordt gesynchroniseerd", f.Dir)
		}
	}
	return clean, nil
}

// within reports whether a is b or inside b.
func within(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
