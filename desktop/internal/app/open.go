package app

import (
	"errors"
	"net/url"
	"os/exec"
	"runtime"
)

// openURL shows a web page or a folder with the desktop's default program.
// Only http(s) and file URLs are opened, passed as one argument without a
// shell, so nothing in them can become a command.
func openURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "file") {
		return errors.New("refusing to open this address")
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	cmd := exec.Command(opener, u.String())
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
