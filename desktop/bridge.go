package main

import (
	"context"
	"net/url"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
	"github.com/AlbertovanEckeveld/cloud/desktop/internal/syncer"
)

// Bridge is what the window may call; nothing else of the app is exposed.
type Bridge struct {
	ctx context.Context
	app *app.App
}

func (b *Bridge) Status() app.Status { return b.app.Status() }

func (b *Bridge) SignIn(server string, allowInsecure bool) error {
	return b.app.SignIn(b.ctx, server, allowInsecure, func(u string) { runtime.BrowserOpenURL(b.ctx, u) })
}

func (b *Bridge) SignOut() error { return b.app.SignOut(b.ctx) }

func (b *Bridge) Roots() ([]syncer.Root, error) { return b.app.Roots(b.ctx) }

// ChooseAndAdd asks for a local folder and links it to root.
func (b *Bridge) ChooseAndAdd(root, name string) error {
	dir, err := runtime.OpenDirectoryDialog(b.ctx, runtime.OpenDialogOptions{
		Title:                "Kies een map voor " + name,
		CanCreateDirectories: true,
	})
	if err != nil || dir == "" {
		return err // cancelled: nothing to do
	}
	return b.app.AddFolder(root, name, dir)
}

func (b *Bridge) RemoveFolder(root string) error { return b.app.RemoveFolder(root) }

func (b *Bridge) SyncNow() { b.app.Kick() }

func (b *Bridge) ConfirmMassDelete(root string) { b.app.ConfirmMassDelete(root) }

func (b *Bridge) SetAutostart(on bool) error { return b.app.SetAutostart(on) }

// OpenFolder shows a synced folder in the file manager. Only folders the
// app syncs are opened, never an arbitrary path from the window.
func (b *Bridge) OpenFolder(dir string) {
	for _, f := range b.app.Status().Folders {
		if f.Dir == dir {
			runtime.BrowserOpenURL(b.ctx, (&url.URL{Scheme: "file", Path: dir}).String())
			return
		}
	}
}

func (b *Bridge) Quit() { runtime.Quit(b.ctx) }
