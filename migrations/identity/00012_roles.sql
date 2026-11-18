-- Roles replace the administrator flag (ADR 0022). A role bundles fixed
-- permission names (defined in code); it is given to users directly or to
-- groups, whose members then have it for as long as they are members.
-- The built-in role "Beheerder" has every permission, also future ones, and
-- cannot be changed. Its ID is fixed, so every site knows it.

-- +goose Up
CREATE TABLE identity.roles (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 60),
    description text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 300),
    permissions text[]      NOT NULL DEFAULT '{}',
    builtin     boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);

CREATE TABLE identity.role_assignments (
    role_id    uuid        NOT NULL REFERENCES identity.roles (id) ON DELETE CASCADE,
    user_id    uuid        REFERENCES identity.users (id) ON DELETE CASCADE,
    group_id   uuid        REFERENCES identity.groups (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    CHECK ((user_id IS NULL) <> (group_id IS NULL))
);
CREATE UNIQUE INDEX role_assignments_user_idx ON identity.role_assignments (user_id, role_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX role_assignments_group_idx ON identity.role_assignments (group_id, role_id) WHERE group_id IS NOT NULL;

INSERT INTO identity.roles (id, name, description, builtin, created_at, updated_at)
VALUES ('01a1f100-0000-7000-8000-000000000001', 'Beheerder', 'Alle rechten, ook rechten die later bijkomen.', true, now(), now());

INSERT INTO identity.role_assignments (role_id, user_id, created_at)
SELECT '01a1f100-0000-7000-8000-000000000001', id, now() FROM identity.users WHERE is_admin;

ALTER TABLE identity.users DROP COLUMN is_admin;

-- +goose Down
ALTER TABLE identity.users ADD COLUMN is_admin boolean NOT NULL DEFAULT false;
UPDATE identity.users SET is_admin = true
WHERE id IN (SELECT user_id FROM identity.role_assignments
             WHERE role_id = '01a1f100-0000-7000-8000-000000000001' AND user_id IS NOT NULL);
DROP TABLE identity.role_assignments;
DROP TABLE identity.roles;
