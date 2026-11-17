package main

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/ipc"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/syncer"
)

//go:embed all:frontend/dist
var assets embed.FS

// runWindow shows the settings window; it talks to the service, starting
// it first if it does not run yet.
func runWindow(log *slog.Logger) error {
	socket, err := app.SocketPath()
	if err != nil {
		return err
	}
	client := ipc.NewClient(socket)
	if !client.Running() {
		if err := spawn("--background"); err != nil {
			return err
		}
		for i := 0; i < 50 && !client.Running(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if !client.Running() {
			return errors.New("the Cloud service did not start")
		}
	}
	b := &Bridge{svc: client}
	return wails.Run(&options.App{
		Title:       "Cloud",
		Width:       760,
		Height:      640,
		AssetServer: &assetserver.Options{Assets: assets},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "nl.albertove.cloud-desktop.settings",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				if b.ctx != nil {
					runtime.WindowShow(b.ctx)
				}
			},
		},
		OnStartup: func(ctx context.Context) { b.ctx = ctx },
		Bind:      []any{b},
	})
}

// Bridge is what the window may call; every call goes to the service.
type Bridge struct {
	ctx context.Context
	svc *ipc.Client
}

func (b *Bridge) call(timeout time.Duration, method string, args, out any) error {
	ctx, cancel := context.WithTimeout(b.ctx, timeout)
	defer cancel()
	return b.svc.Call(ctx, method, args, out)
}

func (b *Bridge) Status() (app.Status, error) {
	var s app.Status
	err := b.call(5*time.Second, "Status", nil, &s)
	return s, err
}

func (b *Bridge) SignIn(server string, allowInsecure bool) error {
	// The user signs in in the browser; give them time.
	return b.call(11*time.Minute, "SignIn", map[string]any{"server": server, "allow_insecure": allowInsecure}, nil)
}

func (b *Bridge) SignOut() error { return b.call(30*time.Second, "SignOut", nil, nil) }

func (b *Bridge) Roots() ([]syncer.Root, error) {
	var roots []syncer.Root
	err := b.call(30*time.Second, "Roots", nil, &roots)
	return roots, err
}

// ChooseAndAdd asks where to put the folder and links root to a folder
// named after it there (e.g. ~/Cloud/Mijn bestanden).
func (b *Bridge) ChooseAndAdd(root, name string) error {
	// Suggest ~/Cloud as the home for synced folders.
	suggest := ""
	if home, err := os.UserHomeDir(); err == nil {
		suggest = filepath.Join(home, "Cloud")
		if err := os.MkdirAll(suggest, 0o700); err != nil {
			suggest = ""
		}
	}
	dir, err := runtime.OpenDirectoryDialog(b.ctx, runtime.OpenDialogOptions{
		Title:                "Waar moet de map \"" + name + "\" komen?",
		DefaultDirectory:     suggest,
		CanCreateDirectories: true,
	})
	if err != nil || dir == "" {
		return err // cancelled: nothing to do
	}
	return b.call(10*time.Second, "AddFolder", map[string]string{"root": root, "name": name, "dir": dir}, nil)
}

func (b *Bridge) RemoveFolder(root string) error {
	return b.call(10*time.Second, "RemoveFolder", map[string]string{"root": root}, nil)
}

func (b *Bridge) SyncNow() error { return b.call(5*time.Second, "SyncNow", nil, nil) }

func (b *Bridge) SetPaused(on bool) error {
	return b.call(5*time.Second, "SetPaused", map[string]bool{"on": on}, nil)
}

func (b *Bridge) ConfirmMassDelete(root string) error {
	return b.call(5*time.Second, "ConfirmMassDelete", map[string]string{"root": root}, nil)
}

func (b *Bridge) SetSidebar(root string, on bool) error {
	return b.call(10*time.Second, "SetSidebar", map[string]any{"root": root, "on": on}, nil)
}

func (b *Bridge) SetAutostart(on bool) error {
	return b.call(10*time.Second, "SetAutostart", map[string]bool{"on": on}, nil)
}

func (b *Bridge) OpenFolder(dir string) error {
	return b.call(5*time.Second, "OpenFolder", map[string]string{"dir": dir}, nil)
}
