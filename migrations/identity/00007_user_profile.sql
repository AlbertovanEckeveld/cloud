-- Profile details the user maintains, shared with apps as OIDC claims
-- (given_name, family_name, locale, picture). The avatar is an object in S3
-- under an unguessable, never reused key.

-- +goose Up
ALTER TABLE identity.users
    ADD COLUMN given_name  text NOT NULL DEFAULT '' CHECK (length(given_name) <= 100),
    ADD COLUMN family_name text NOT NULL DEFAULT '' CHECK (length(family_name) <= 100),
    ADD COLUMN locale      text NOT NULL DEFAULT 'nl' CHECK (locale IN ('nl', 'en')),
    ADD COLUMN avatar_key  text UNIQUE;

-- +goose Down
ALTER TABLE identity.users DROP COLUMN avatar_key, DROP COLUMN locale, DROP COLUMN family_name, DROP COLUMN given_name;
