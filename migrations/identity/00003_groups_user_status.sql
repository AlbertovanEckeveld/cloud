-- +goose Up

-- A disabled account cannot sign in; its sessions are revoked when disabled.
ALTER TABLE identity.users ADD COLUMN disabled_at timestamptz;
ALTER TABLE identity.users ADD COLUMN last_login_at timestamptz;
UPDATE identity.users u SET last_login_at = (
    SELECT max(occurred_at) FROM identity.audit_events e
    WHERE e.actor_id = u.id AND e.event = 'auth.login.succeeded');

-- Groups bundle users, e.g. for the OIDC "groups" claim. The name is a
-- stable identifier that external apps map to roles, so it follows the
-- same rules as usernames; the description is free text.
CREATE TABLE identity.groups (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,31}$'),
    description text        NOT NULL DEFAULT '' CHECK (length(description) <= 200),
    created_at  timestamptz NOT NULL
);

CREATE TABLE identity.group_members (
    group_id uuid        NOT NULL REFERENCES identity.groups (id) ON DELETE CASCADE,
    user_id  uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_id_idx ON identity.group_members (user_id);

-- +goose Down
DROP TABLE identity.group_members;
DROP TABLE identity.groups;
ALTER TABLE identity.users DROP COLUMN last_login_at;
ALTER TABLE identity.users DROP COLUMN disabled_at;
