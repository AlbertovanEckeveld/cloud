-- Team folders: folders an administrator creates for groups, owned by no
-- person. The team folder itself is the owner (drive.nodes.owner_id = id) of
-- everything in it, so quota, trash and moves work per team folder exactly as
-- they do per user. Access is granted to groups on the root folder through the
-- sharing model of the identity module (ADR 0011).

-- +goose Up
CREATE TABLE drive.team_folders (
    id          uuid        PRIMARY KEY,
    -- The single top-level folder; deleting it for good removes the team folder.
    root_id     uuid        NOT NULL UNIQUE REFERENCES drive.nodes (id) ON DELETE CASCADE,
    limit_bytes bigint      CHECK (limit_bytes >= 0), -- NULL: unlimited
    created_by  uuid        NOT NULL,
    created_at  timestamptz NOT NULL
);

-- +goose Down
DROP TABLE drive.team_folders;
