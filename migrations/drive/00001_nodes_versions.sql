-- Files and folders. Content lives in S3 (one immutable object per version);
-- the database holds the tree, names and versions. owner_id refers to an
-- identity user; modules do not share tables, so there is no foreign key.

-- +goose Up
CREATE TABLE drive.nodes (
    id                 uuid        PRIMARY KEY,
    owner_id           uuid        NOT NULL,
    parent_id          uuid        REFERENCES drive.nodes (id) ON DELETE CASCADE, -- NULL: top level
    kind               text        NOT NULL CHECK (kind IN ('folder', 'file')),
    name               text        NOT NULL CHECK (length(name) BETWEEN 1 AND 255 AND name NOT IN ('.', '..')
                                                   AND strpos(name, '/') = 0 AND strpos(name, E'\\') = 0),
    size               bigint      NOT NULL DEFAULT 0 CHECK (size >= 0),
    content_type       text        NOT NULL DEFAULT '',
    current_version_id uuid,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    -- Set on the node the user deleted; its descendants are hidden with it.
    trashed_at         timestamptz
);
-- One name per folder, ignoring case, so case-insensitive clients (Windows,
-- macOS) never see two entries they cannot tell apart.
CREATE UNIQUE INDEX nodes_name_key ON drive.nodes
    (owner_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'), lower(name))
    WHERE trashed_at IS NULL;
CREATE INDEX nodes_parent_idx ON drive.nodes (parent_id);
CREATE INDEX nodes_trash_idx ON drive.nodes (owner_id, trashed_at) WHERE trashed_at IS NOT NULL;

CREATE TABLE drive.versions (
    id           uuid        PRIMARY KEY,
    node_id      uuid        NOT NULL REFERENCES drive.nodes (id) ON DELETE CASCADE,
    object_key   text        NOT NULL UNIQUE,
    size         bigint      NOT NULL CHECK (size >= 0),
    sha256       bytea       NOT NULL CHECK (length(sha256) = 32),
    content_type text        NOT NULL,
    created_at   timestamptz NOT NULL,
    created_by   uuid        NOT NULL
);
CREATE INDEX versions_node_idx ON drive.versions (node_id, created_at DESC);

-- Per-user storage limit; no row (or NULL) means unlimited.
CREATE TABLE drive.quotas (
    user_id     uuid   PRIMARY KEY,
    limit_bytes bigint CHECK (limit_bytes >= 0)
);

-- S3 objects to delete. Objects are removed only after the database no
-- longer references them; failures are retried, so nothing is orphaned.
CREATE TABLE drive.object_deletions (
    object_key      text        PRIMARY KEY,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE drive.object_deletions;
DROP TABLE drive.quotas;
DROP TABLE drive.versions;
DROP TABLE drive.nodes;
