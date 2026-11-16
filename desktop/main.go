// Command cloud-desktop is the Cloud desktop app (ADR 0016). It runs as a
// background service with a tray icon that keeps folders in sync, and opens
// its settings window as a separate process on request: the tray and the
// window each need the main event loop, so they never share a process.
package main

import (
	"flag"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
)

// The tray (macOS) and the window must run on the main thread.
func init() { runtime.LockOSThread() }

func main() {
	settings := flag.Bool("settings", false, "open the settings window")
	background := flag.Bool("background", false, "start the service without opening the window (autostart)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var err error
	if *settings {
		err = runWindow(log)
	} else {
		err = runService(log, *background)
	}
	if err != nil {
		log.Error("cloud-desktop stopped", "error", err)
		os.Exit(1)
	}
}

// spawn starts this program again with args, detached from this one.
func spawn(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
