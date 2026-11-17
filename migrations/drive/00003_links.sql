-- Public share links (ADR 0012): read-only access to a file or folder for
-- anyone who has the link. Only a SHA-256 hash of the token is stored.

-- +goose Up
CREATE TABLE drive.links (
    id              uuid        PRIMARY KEY,
    node_id         uuid        NOT NULL REFERENCES drive.nodes (id) ON DELETE CASCADE,
    token_hash      bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    password_hash   text,       -- argon2id; NULL: no password
    expires_at      timestamptz,
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL,
    downloads       integer     NOT NULL DEFAULT 0,
    failed_attempts integer     NOT NULL DEFAULT 0,
    locked_until    timestamptz
);
CREATE INDEX links_node_idx ON drive.links (node_id);

-- +goose Down
DROP TABLE drive.links;
