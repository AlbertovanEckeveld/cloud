-- OpenID Connect provider: registered apps (clients), signing keys, pending
-- authorization requests and issued tokens. Secrets and tokens are only
-- stored as SHA-256 hashes; private keys are encrypted with the platform key.

-- +goose Up
CREATE TABLE identity.oidc_clients (
    id                        text        PRIMARY KEY,
    name                      text        NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    secret_hash               bytea       CHECK (length(secret_hash) = 32), -- NULL: public client (PKCE)
    redirect_uris             text[]      NOT NULL CHECK (cardinality(redirect_uris) > 0),
    post_logout_redirect_uris text[]      NOT NULL DEFAULT '{}',
    -- Only members of this group may sign in. RESTRICT: deleting the group
    -- must never silently open the app to everyone.
    allowed_group_id          uuid        REFERENCES identity.groups (id) ON DELETE RESTRICT,
    created_at                timestamptz NOT NULL
);

CREATE TABLE identity.oidc_signing_keys (
    id          text        PRIMARY KEY,
    private_key bytea       NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE TABLE identity.oidc_auth_requests (
    id         uuid        PRIMARY KEY,
    client_id  text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    request    jsonb       NOT NULL,
    user_id    uuid        REFERENCES identity.users (id) ON DELETE CASCADE,
    auth_time  timestamptz,
    code_hash  bytea       UNIQUE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE identity.oidc_refresh_tokens (
    id         uuid        PRIMARY KEY,
    token_hash bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    client_id  text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    scopes     text[]      NOT NULL,
    auth_time  timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX oidc_refresh_tokens_user_idx ON identity.oidc_refresh_tokens (user_id);

CREATE TABLE identity.oidc_access_tokens (
    id               uuid        PRIMARY KEY,
    client_id        text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    user_id          uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    scopes           text[]      NOT NULL,
    refresh_token_id uuid        REFERENCES identity.oidc_refresh_tokens (id) ON DELETE CASCADE,
    expires_at       timestamptz NOT NULL
);
CREATE INDEX oidc_access_tokens_user_idx ON identity.oidc_access_tokens (user_id);

-- +goose Down
DROP TABLE identity.oidc_access_tokens;
DROP TABLE identity.oidc_refresh_tokens;
DROP TABLE identity.oidc_auth_requests;
DROP TABLE identity.oidc_signing_keys;
DROP TABLE identity.oidc_clients;
