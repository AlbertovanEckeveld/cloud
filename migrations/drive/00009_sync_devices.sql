-- What each linked desktop app synchronises, as it reports after every
-- round: the roots ("mine" or a folder ID) and when it last finished.
-- device_id is identity's device (its refresh token); identity decides
-- whether a device still exists, so rows of unlinked devices are simply
-- never shown and are pruned after 90 days without a report.

-- +goose Up
CREATE TABLE drive.sync_devices (
    device_id    uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL,
    roots        text[]      NOT NULL CHECK (cardinality(roots) <= 100),
    last_sync_at timestamptz NOT NULL
);
CREATE INDEX sync_devices_user_idx ON drive.sync_devices (user_id);

-- +goose Down
DROP TABLE drive.sync_devices;
