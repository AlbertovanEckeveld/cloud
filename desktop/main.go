// Command cloud-desktop is the Cloud desktop app: it keeps chosen folders
// in sync with Cloud (ADR 0016).
package main

import (
	"context"
	"embed"
	"flag"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	hidden := flag.Bool("hidden", false, "start without showing the window (autostart)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	b := &Bridge{}
	a, err := app.New(log, func(s app.Status) {
		if b.ctx != nil {
			runtime.EventsEmit(b.ctx, "status", s)
		}
	})
	if err != nil {
		log.Error("cannot start", "error", err)
		os.Exit(1)
	}
	b.app = a

	err = wails.Run(&options.App{
		Title:             "Cloud",
		Width:             760,
		Height:            620,
		AssetServer:       &assetserver.Options{Assets: assets},
		StartHidden:       *hidden,
		HideWindowOnClose: true, // keep syncing in the background
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "nl.albertove.cloud-desktop",
			// Starting the app again brings the window back.
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				if b.ctx != nil {
					runtime.WindowShow(b.ctx)
				}
			},
		},
		OnStartup: func(ctx context.Context) {
			b.ctx = ctx
			a.Start(ctx)
		},
		OnShutdown: func(context.Context) { a.Close() },
		Bind:       []any{b},
	})
	if err != nil {
		log.Error("window failed", "error", err)
		os.Exit(1)
	}
}
