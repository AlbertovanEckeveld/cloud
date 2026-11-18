//go:build integration

package drive

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/authz"
)

func TestSyncDevices(t *testing.T) {
	e := newTestEnv(t)
	owner, other := e.person(t, "Ann"), e.person(t, "Bea")
	device, otherDevice := newID(), newID()

	resp, data := e.request(t, owner, http.MethodPost, "/folders", strings.NewReader(`{"parent_id":"root","name":"Projecten"}`),
		"Content-Type", "application/json")
	var folder Node
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &folder) != nil {
		t.Fatalf("mkdir: %d %s", resp.StatusCode, data)
	}
	if st := e.share(t, owner, folder.ID, other, authz.Edit); st != http.StatusOK {
		t.Fatalf("share: %d", st)
	}

	report := func(user uuid.UUID, dev, body string) int {
		resp, _ := e.request(t, user, http.MethodPut, "/sync/device", strings.NewReader(body),
			"Content-Type", "application/json", "X-Test-Device", dev)
		return resp.StatusCode
	}
	list := func(user uuid.UUID) []SyncDevice {
		resp, data := e.request(t, user, http.MethodGet, "/sync/devices", nil)
		var out struct{ Devices []SyncDevice }
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &out) != nil {
			t.Fatalf("list: %d %s", resp.StatusCode, data)
		}
		return out.Devices
	}

	// Only a device reports, and only "mine" or folder IDs.
	if st := report(owner, "", `{"roots":["mine"]}`); st != http.StatusForbidden {
		t.Errorf("report without a device: %d", st)
	}
	if st := report(owner, device.String(), `{"roots":["../etc"]}`); st != http.StatusUnprocessableEntity {
		t.Errorf("report with a bad root: %d", st)
	}

	// The other user syncs their own files, the shared folder and a folder
	// they cannot see (which is never shown).
	unknown := newID().String()
	body := `{"roots":["mine","` + folder.ID.String() + `","` + unknown + `","mine"]}`
	if st := report(other, otherDevice.String(), body); st != http.StatusNoContent {
		t.Fatalf("report: %d", st)
	}
	got := list(other)
	if len(got) != 1 || got[0].DeviceID != otherDevice || time.Since(got[0].LastSyncAt) > time.Minute {
		t.Fatalf("devices: %+v", got)
	}
	names := []string{}
	for _, f := range got[0].Folders {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "Mijn bestanden,Projecten" {
		t.Fatalf("folders: %v", names)
	}

	// Someone else's report for that device ID changes nothing, and devices
	// are only listed for their own user.
	if st := report(owner, otherDevice.String(), `{"roots":[]}`); st != http.StatusNoContent {
		t.Fatalf("report by another user: %d", st)
	}
	if again := list(other); len(again) != 1 || len(again[0].Folders) != 2 {
		t.Fatalf("device taken over: %+v", again)
	}
	if mine := list(owner); len(mine) != 0 {
		t.Fatalf("owner sees: %+v", mine)
	}
}
