package app

import (
	"context"
	"encoding/json"
	"errors"
)

// Handle answers the settings window (through ipc). Only these calls exist;
// every argument is decoded into a fixed type.
func (a *App) Handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var args struct {
		Server        string `json:"server"`
		AllowInsecure bool   `json:"allow_insecure"`
		Root          string `json:"root"`
		Name          string `json:"name"`
		Dir           string `json:"dir"`
		On            bool   `json:"on"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, errors.New("invalid request")
		}
	}
	switch method {
	case "Status":
		return a.Status(), nil
	case "SignIn":
		return nil, a.SignIn(ctx, args.Server, args.AllowInsecure, func(u string) { _ = openURL(u) })
	case "SignOut":
		return nil, a.SignOut(ctx)
	case "Roots":
		return a.Roots(ctx)
	case "AddFolder":
		return nil, a.AddFolder(args.Root, args.Name, args.Dir)
	case "RemoveFolder":
		return nil, a.RemoveFolder(args.Root)
	case "SyncNow":
		a.Kick()
		return nil, nil
	case "SetPaused":
		a.SetPaused(args.On)
		return nil, nil
	case "ConfirmMassDelete":
		a.ConfirmMassDelete(args.Root)
		return nil, nil
	case "SetSidebar":
		return nil, a.SetSidebar(args.Root, args.On)
	case "SetAutostart":
		return nil, a.SetAutostart(args.On)
	case "OpenFolder":
		return nil, a.OpenFolder(args.Dir)
	}
	return nil, errors.New("unknown request")
}
