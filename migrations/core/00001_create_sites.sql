-- Every platform instance registers its site here and refreshes last_seen_at
-- periodically. Once sites replicate, this shows which sites are alive and
-- which version each one runs.
--
-- The primary key is the operator-chosen SITE_ID rather than a UUID: it is a
-- stable, human-readable natural key that never needs to be generated.

-- +goose Up
CREATE TABLE core.sites (
    id            text        PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    version       text        NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE core.sites;
