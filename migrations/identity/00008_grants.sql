-- The sharing model (ADR 0006): who may do what with resources owned by
-- other modules. resource_id belongs to the module named in resource_type,
-- so there is no foreign key; modules remove grants of deleted resources.

-- +goose Up
CREATE TABLE identity.grants (
    resource_type text        NOT NULL CHECK (resource_type ~ '^[a-z]+\.[a-z_]+$'),
    resource_id   uuid        NOT NULL,
    subject_type  text        NOT NULL CHECK (subject_type IN ('user', 'group')),
    subject_id    uuid        NOT NULL,
    permission    text        NOT NULL CHECK (permission IN ('view', 'edit', 'manage')),
    granted_by    uuid        NOT NULL,
    granted_at    timestamptz NOT NULL,
    PRIMARY KEY (resource_type, resource_id, subject_type, subject_id)
);
CREATE INDEX grants_subject_idx ON identity.grants (subject_type, subject_id, resource_type);

-- +goose Down
DROP TABLE identity.grants;
