package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/ipc"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/tray"
)

// runService runs the sync service with its tray icon. If a service already
// runs, this start only opens the settings window.
func runService(log *slog.Logger, background bool) error {
	socket, err := app.SocketPath()
	if err != nil {
		return err
	}
	if ipc.NewClient(socket).Running() {
		if background {
			return nil
		}
		return spawn("--settings")
	}

	var (
		mu sync.Mutex
		t  *tray.Tray
	)
	a, err := app.New(log, func(s app.Status) {
		mu.Lock()
		defer mu.Unlock()
		if t != nil {
			t.Update(s)
		}
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := ipc.Listen(ctx, socket, a.Handle, log); err != nil {
		if errors.Is(err, ipc.ErrRunning) {
			return spawn("--settings")
		}
		return err
	}
	a.Start(ctx)
	defer a.Close()
	// Logging out or shutting down sends SIGTERM: stop cleanly, so the state
	// database is closed and the socket removed.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-signals
		cancel()
		tray.Stop()
	}()

	openSettings := func() {
		if err := spawn("--settings"); err != nil {
			log.Warn("cannot open the settings window", "error", err)
		}
	}
	tray.Run(tray.Actions{
		SyncNow:      a.Kick,
		SetPaused:    a.SetPaused,
		OpenSettings: openSettings,
		Quit:         cancel,
	}, func(tr *tray.Tray) {
		mu.Lock()
		t = tr
		mu.Unlock()
		tr.Update(a.Status())
		// The first start (not signed in yet) shows the window right away.
		if !background && !a.Status().SignedIn {
			openSettings()
		}
	})
	return nil
}
