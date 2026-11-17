package mailer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// writeToDir returns a sendFunc that stores each message as an .eml file in
// dir instead of sending it (MAIL_DEV_DIR, development only). Files are
// readable by the owner only: they contain activation links. The file is
// written under a temporary name and renamed, so a mail program watching
// the directory never sees half a message.
func (m *Mailer) writeToDir(dir string) sendFunc {
	return func(_ context.Context, _, _ string, raw []byte) error {
		suffix := make([]byte, 4)
		if _, err := rand.Read(suffix); err != nil {
			return fmt.Errorf("mail file name: %w", err)
		}
		name := m.now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix) + ".eml"
		final := filepath.Join(dir, name)
		tmp := final + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			return fmt.Errorf("write mail file: %w", err)
		}
		if err := os.Rename(tmp, final); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("write mail file: %w", err)
		}
		m.log.Info("mail written to MAIL_DEV_DIR instead of sent", "file", final)
		return nil
	}
}

// prepareDir creates dir if needed, for the owner only, and checks that it
// is a directory.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("MAIL_DEV_DIR: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("MAIL_DEV_DIR: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("MAIL_DEV_DIR: %s is not a directory", dir)
	}
	return nil
}
