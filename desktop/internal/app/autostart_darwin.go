package app

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// setAutostart writes or removes a LaunchAgent that starts the app at login.
func setAutostart(on bool, exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "nl.albertove.cloud-desktop.plist")
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, []byte(exe)); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>nl.albertove.cloud-desktop</string>
  <key>ProgramArguments</key><array><string>` + escaped.String() + `</string><string>--background</string></array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}
