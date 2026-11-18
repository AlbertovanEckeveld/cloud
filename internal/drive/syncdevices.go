package drive

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

const (
	maxSyncRoots        = 100
	syncDeviceRetention = 90 * 24 * time.Hour
)

// SyncDevice is what one linked desktop app synchronises.
type SyncDevice struct {
	DeviceID   uuid.UUID  `json:"device_id"`
	LastSyncAt time.Time  `json:"last_sync_at"`
	Folders    []SyncRoot `json:"folders"`
}

// ReportSync records that a device finished a round over these roots.
// Roots are only stored here; they are resolved (and checked against what
// the user may still see) when they are listed.
func (s *Service) ReportSync(ctx context.Context, userID, deviceID uuid.UUID, roots []string) error {
	if len(roots) > maxSyncRoots {
		return &InputError{Reason: "Te veel mappen."}
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, r := range roots {
		if r != mineRoot {
			id, err := uuid.Parse(r)
			if err != nil {
				return &InputError{Reason: "Onbekende map."}
			}
			r = id.String()
		}
		if !seen[r] {
			seen[r] = true
			clean = append(clean, r)
		}
	}
	// A device ID belongs to one user; another user's report never
	// overwrites it.
	if _, err := s.db.Exec(ctx, `
		INSERT INTO drive.sync_devices (device_id, user_id, roots, last_sync_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (device_id) DO UPDATE SET roots = EXCLUDED.roots, last_sync_at = EXCLUDED.last_sync_at
		WHERE drive.sync_devices.user_id = EXCLUDED.user_id`, deviceID, userID, clean, s.now()); err != nil {
		return fmt.Errorf("report sync: %w", err)
	}
	return nil
}

// SyncDevices lists what the user's devices synchronise, with the folder
// names the user can see now.
func (s *Service) SyncDevices(ctx context.Context, userID uuid.UUID) ([]SyncDevice, error) {
	rows, err := s.db.Query(ctx, `
		SELECT device_id, roots, last_sync_at FROM drive.sync_devices
		WHERE user_id = $1 ORDER BY last_sync_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sync devices: %w", err)
	}
	type stored struct {
		id    uuid.UUID
		roots []string
		at    time.Time
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
		var d stored
		return d, row.Scan(&d.id, &d.roots, &d.at)
	})
	if err != nil {
		return nil, fmt.Errorf("read sync devices: %w", err)
	}
	visible, err := s.SyncRoots(ctx, userID)
	if err != nil {
		return nil, err
	}
	byID := map[string]SyncRoot{}
	for _, r := range visible {
		byID[r.ID] = r
	}
	out := make([]SyncDevice, 0, len(all))
	for _, d := range all {
		dev := SyncDevice{DeviceID: d.id, LastSyncAt: d.at.UTC(), Folders: []SyncRoot{}}
		for _, id := range d.roots {
			if r, ok := byID[id]; ok {
				dev.Folders = append(dev.Folders, r)
			}
		}
		out = append(out, dev)
	}
	return out, nil
}

func (s *Service) pruneSyncDevices(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM drive.sync_devices WHERE last_sync_at < $1`, s.now().Add(-syncDeviceRetention)); err != nil {
		return fmt.Errorf("prune sync devices: %w", err)
	}
	return nil
}

func (s *Service) handleReportSync(w http.ResponseWriter, r *http.Request) {
	device := s.deviceID(r)
	if device == uuid.Nil {
		httpserver.WriteError(w, http.StatusForbidden, "forbidden", "Alleen de desktop-app meldt synchronisaties.")
		return
	}
	var in struct {
		Roots []string `json:"roots"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	if err := s.ReportSync(r.Context(), s.userID(r), device, in.Roots); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSyncDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.SyncDevices(r.Context(), s.userID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Devices []SyncDevice `json:"devices"`
	}{devices})
}
