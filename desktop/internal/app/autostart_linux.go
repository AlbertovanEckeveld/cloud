package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// setAutostart writes or removes an XDG autostart entry.
func setAutostart(on bool, exe string) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	p := filepath.Join(base, "autostart", "cloud-desktop.desktop")
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Exec quoting per the Desktop Entry spec: the path is one argument.
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(exe) + `"`
	entry := "[Desktop Entry]\nType=Application\nName=Cloud\nExec=" + quoted + " --hidden\nX-GNOME-Autostart-enabled=true\n"
	return os.WriteFile(p, []byte(entry), 0o644)
}
