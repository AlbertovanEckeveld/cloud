package identity

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// throttleRule limits failed attempts per key. After threshold failures
// within window, the key is locked; each further failure doubles the lock,
// up to maxLock. Doubling slows brute force down sharply while a legitimate
// user who mistypes a few times is barely affected.
type throttleRule struct {
	prefix    string
	threshold int
	window    time.Duration
	maxLock   time.Duration
}

var (
	throttleUser = throttleRule{prefix: "user:", threshold: 5, window: 15 * time.Minute, maxLock: 15 * time.Minute}
	throttleIP   = throttleRule{prefix: "ip:", threshold: 20, window: 15 * time.Minute, maxLock: 15 * time.Minute}
	throttleTOTP = throttleRule{prefix: "totp:", threshold: 5, window: 15 * time.Minute, maxLock: 15 * time.Minute}
)

func (r throttleRule) key(id string) string {
	// Keys can contain attacker-chosen usernames; bound their size.
	if len(id) > 64 {
		id = id[:64]
	}
	return r.prefix + strings.ToLower(id)
}

// ThrottledError means too many failed attempts; retry after RetryAfter.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed attempts, retry after %s", e.RetryAfter)
}

// checkThrottle returns a ThrottledError if any of the keys is locked.
func (s *Service) checkThrottle(ctx context.Context, keys ...string) error {
	var lockedUntil *time.Time
	err := s.db.QueryRow(ctx,
		`SELECT max(locked_until) FROM identity.throttle WHERE key = ANY($1) AND locked_until > $2`,
		keys, s.now()).Scan(&lockedUntil)
	if err != nil {
		return fmt.Errorf("check throttle: %w", err)
	}
	if lockedUntil != nil {
		return &ThrottledError{RetryAfter: lockedUntil.Sub(s.now()).Round(time.Second)}
	}
	return nil
}

// recordFailure counts a failed attempt and reports whether the key is now locked.
func (s *Service) recordFailure(ctx context.Context, rule throttleRule, key string) (bool, error) {
	now := s.now()
	var failures int
	err := s.db.QueryRow(ctx, `
		INSERT INTO identity.throttle AS t (key, failures, last_failure_at)
		VALUES ($1, 1, $2)
		ON CONFLICT (key) DO UPDATE SET
			failures = CASE WHEN t.last_failure_at < $2 - $3::interval THEN 1 ELSE t.failures + 1 END,
			last_failure_at = $2
		RETURNING failures`,
		key, now, rule.window).Scan(&failures)
	if err != nil {
		return false, fmt.Errorf("record failure: %w", err)
	}
	if failures < rule.threshold {
		return false, nil
	}
	lock := time.Minute << min(failures-rule.threshold, 10)
	lock = min(lock, rule.maxLock)
	if _, err := s.db.Exec(ctx, `UPDATE identity.throttle SET locked_until = $2 WHERE key = $1`, key, now.Add(lock)); err != nil {
		return false, fmt.Errorf("lock key: %w", err)
	}
	return true, nil
}

// recordFailures counts a failure for several keys; errors are logged, not
// returned, because the caller is already reporting a failure.
func (s *Service) recordFailures(ctx context.Context, failures map[string]throttleRule) {
	for key, rule := range failures {
		if _, err := s.recordFailure(ctx, rule, key); err != nil {
			s.log.Error("throttle bookkeeping failed", "error", err)
		}
	}
}

func (s *Service) clearThrottle(ctx context.Context, key string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM identity.throttle WHERE key = $1`, key); err != nil {
		return fmt.Errorf("clear throttle: %w", err)
	}
	return nil
}
