package tray

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
)

func TestSummary(t *testing.T) {
	ok := app.Status{SignedIn: true, Folders: []app.FolderStatus{{Folder: app.Folder{Name: "Mijn bestanden"}, LastRun: time.Now()}}}
	for name, tc := range map[string]struct {
		s    app.Status
		want string
	}{
		"signed out": {app.Status{}, "Niet gekoppeld"},
		"ok":         {ok, "Alles bijgewerkt"},
		"syncing":    {func() app.Status { s := ok; s.Syncing = true; return s }(), "Bezig met synchroniseren…"},
		"paused":     {func() app.Status { s := ok; s.Paused = true; return s }(), "Gepauzeerd"},
		"brake": {app.Status{SignedIn: true, Folders: []app.FolderStatus{{MassDelete: &app.MassDeleteAsk{Local: 30}}}},
			"Wacht op je bevestiging (veel verwijderingen)"},
	} {
		if got, _ := summary(tc.s); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestIconsArePNG(t *testing.T) {
	if _, err := png.Decode(bytes.NewReader(dot(green))); err != nil {
		t.Fatal(err)
	}
}
