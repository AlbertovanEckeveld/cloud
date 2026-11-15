package drive

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Usage is how much storage a user uses and may use.
type Usage struct {
	UsedBytes  int64  `json:"used_bytes"`
	LimitBytes *int64 `json:"limit_bytes"` // nil: unlimited
}

// usage counts every stored version, including those in the trash: they
// take up space until purged. ownerID is a user or a team folder.
func (s *Service) usage(ctx context.Context, q dbtx, userID uuid.UUID) (int64, *int64, error) {
	var used int64
	var limit *int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE((SELECT sum(v.size) FROM drive.versions v JOIN drive.nodes n ON n.id = v.node_id WHERE n.owner_id = $1), 0),
		       COALESCE((SELECT limit_bytes FROM drive.quotas WHERE user_id = $1),
		                (SELECT limit_bytes FROM drive.team_folders WHERE id = $1))`, userID).Scan(&used, &limit)
	if err != nil {
		return 0, nil, fmt.Errorf("load usage: %w", err)
	}
	return used, limit, nil
}

// Usage returns a user's storage use and limit.
func (s *Service) Usage(ctx context.Context, userID uuid.UUID) (Usage, error) {
	used, limit, err := s.usage(ctx, s.db, userID)
	return Usage{UsedBytes: used, LimitBytes: limit}, err
}

// SetQuota sets a user's limit; nil removes it.
func (s *Service) SetQuota(ctx context.Context, userID uuid.UUID, limit *int64) error {
	if limit != nil && *limit < 0 {
		return &InputError{"Het quotum kan niet negatief zijn."}
	}
	var err error
	if limit == nil {
		_, err = s.db.Exec(ctx, `DELETE FROM drive.quotas WHERE user_id = $1`, userID)
	} else {
		_, err = s.db.Exec(ctx, `
			INSERT INTO drive.quotas (user_id, limit_bytes) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET limit_bytes = EXCLUDED.limit_bytes`, userID, *limit)
	}
	if err != nil {
		return fmt.Errorf("set quota: %w", err)
	}
	return nil
}
