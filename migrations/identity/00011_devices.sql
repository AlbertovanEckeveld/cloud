-- A desktop app's refresh token is one linked device: rotation updates the
-- row in place, so its ID is stable for as long as the device is linked.
-- The app reports its name and platform; last_used_at is refreshed (at
-- most once a minute) whenever one of its access tokens is used.

-- +goose Up
ALTER TABLE identity.oidc_refresh_tokens
    ADD COLUMN device_name     text CHECK (char_length(device_name) BETWEEN 1 AND 64),
    ADD COLUMN device_platform text CHECK (device_platform IN ('linux', 'darwin', 'windows')),
    ADD COLUMN last_used_at    timestamptz;

-- +goose Down
ALTER TABLE identity.oidc_refresh_tokens
    DROP COLUMN last_used_at, DROP COLUMN device_platform, DROP COLUMN device_name;
