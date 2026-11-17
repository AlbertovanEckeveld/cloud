// Package tray shows the service in the system tray: an icon whose colour
// tells the sync status, and a menu with the state per folder and actions.
package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"path"
	"time"

	"fyne.io/systray"

	"github.com/AlbertovanEckeveld/cloud/desktop/internal/app"
)

// maxFolders is how many folders the menu lists by name.
const maxFolders = 8

var (
	green  = color.RGBA{0x3f, 0xb9, 0x50, 0xff}
	blue   = color.RGBA{0x38, 0x8b, 0xfd, 0xff}
	orange = color.RGBA{0xf0, 0x88, 0x3e, 0xff}
	red    = color.RGBA{0xe5, 0x48, 0x4d, 0xff}
	grey   = color.RGBA{0x8b, 0x8b, 0x8b, 0xff}
)

// dot draws a filled circle with a light rim as a PNG.
func dot(c color.RGBA) []byte {
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)-31.5, float64(y)-31.5)
			switch {
			case d <= 24:
				img.Set(x, y, c)
			case d <= 28:
				img.Set(x, y, color.RGBA{0xff, 0xff, 0xff, 0xe0})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Actions are what the menu can trigger.
type Actions struct {
	SyncNow      func()
	SetPaused    func(bool)
	OpenSettings func()
	Quit         func()
}

// Tray is the running tray icon.
type Tray struct {
	icons   map[color.RGBA][]byte
	header  *systray.MenuItem
	folders [maxFolders]*systray.MenuItem
	pause   *systray.MenuItem
	paused  bool
}

// Run shows the icon and blocks until Quit; it must run on the main thread.
// ready receives the tray once it can be updated.
func Run(act Actions, ready func(*Tray)) {
	systray.Run(func() {
		t := &Tray{icons: map[color.RGBA][]byte{}}
		for _, c := range []color.RGBA{green, blue, orange, red, grey} {
			t.icons[c] = dot(c)
		}
		systray.SetIcon(t.icons[grey])
		systray.SetTitle("Cloud")
		systray.SetTooltip("Cloud")
		systray.SetOnTapped(act.OpenSettings) // a left click opens the settings

		t.header = systray.AddMenuItem("Cloud", "")
		t.header.Disable()
		for i := range t.folders {
			t.folders[i] = systray.AddMenuItem("", "")
			t.folders[i].Disable()
			t.folders[i].Hide()
		}
		systray.AddSeparator()
		syncNow := systray.AddMenuItem("Nu synchroniseren", "")
		t.pause = systray.AddMenuItem("Pauzeren", "")
		settings := systray.AddMenuItem("Instellingen openen…", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Afsluiten", "Stopt het synchroniseren")
		go func() {
			for {
				select {
				case <-syncNow.ClickedCh:
					act.SyncNow()
				case <-t.pause.ClickedCh:
					act.SetPaused(!t.paused)
				case <-settings.ClickedCh:
					act.OpenSettings()
				case <-quit.ClickedCh:
					act.Quit()
					systray.Quit()
					return
				}
			}
		}()
		ready(t)
	}, func() {})
}

// Stop ends Run (for example when the system asks the app to quit).
func Stop() { systray.Quit() }

// Update shows a new status.
func (t *Tray) Update(s app.Status) {
	text, c := summary(s)
	systray.SetIcon(t.icons[c])
	systray.SetTooltip("Cloud: " + text)
	t.header.SetTitle(text)
	t.paused = s.Paused
	if s.Paused {
		t.pause.SetTitle("Hervatten")
	} else {
		t.pause.SetTitle("Pauzeren")
	}
	for i, item := range t.folders {
		if i >= len(s.Folders) {
			item.Hide()
			continue
		}
		item.SetTitle(folderLine(s.Folders[i]))
		item.Show()
	}
}

func summary(s app.Status) (string, color.RGBA) {
	attention, failed := 0, 0
	for _, f := range s.Folders {
		if f.MassDelete != nil {
			attention++
		}
		if f.Error != "" {
			failed++
		}
	}
	switch {
	case !s.SignedIn:
		return "Niet gekoppeld", grey
	case s.Error != "":
		return s.Error, red
	case attention > 0:
		return "Wacht op je bevestiging (veel verwijderingen)", orange
	case s.Paused:
		return "Gepauzeerd", orange
	case s.Current != nil && s.Current.Total > 0:
		verb := "Uploaden"
		if s.Current.Op == "download" {
			verb = "Downloaden"
		}
		return fmt.Sprintf("%s %s (%d%%, stap %d van %d)", verb, path.Base(s.Current.Path),
			s.Current.Done*100/s.Current.Total, s.Current.Step, s.Current.Steps), blue
	case s.Syncing:
		return "Bezig met synchroniseren…", blue
	case failed > 0:
		return fmt.Sprintf("%d map(pen) met problemen", failed), red
	case len(s.Folders) == 0:
		return "Nog geen mappen gekozen", grey
	}
	return "Alles bijgewerkt", green
}

func folderLine(f app.FolderStatus) string {
	switch {
	case f.MassDelete != nil:
		return f.Name + ": wacht op bevestiging"
	case f.Error != "":
		return f.Name + ": " + f.Error
	case f.LastRun.IsZero():
		return f.Name + ": nog niet gesynchroniseerd"
	}
	return f.Name + ": bijgewerkt om " + f.LastRun.Local().Format(time.TimeOnly[:5])
}
