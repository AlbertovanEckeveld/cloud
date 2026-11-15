-- How a session was authenticated, passed on to apps as the amr and acr
-- claims (RFC 8176; OpenID EAP ACR values), and back-channel logout.

-- +goose Up
ALTER TABLE identity.sessions
    ADD COLUMN amr text[] NOT NULL DEFAULT '{}',
    ADD COLUMN acr text   NOT NULL DEFAULT '';
ALTER TABLE identity.oidc_auth_requests
    ADD COLUMN amr text[] NOT NULL DEFAULT '{}',
    ADD COLUMN acr text   NOT NULL DEFAULT '';
ALTER TABLE identity.oidc_refresh_tokens
    ADD COLUMN amr text[] NOT NULL DEFAULT '{}',
    ADD COLUMN acr text   NOT NULL DEFAULT '';

-- Where an app wants to hear that a user signed out (OIDC Back-Channel Logout 1.0).
ALTER TABLE identity.oidc_clients ADD COLUMN backchannel_logout_uri text;

-- Which apps a user has signed in to: shown on the account page, and the
-- apps to notify when the user signs out everywhere.
CREATE TABLE identity.oidc_grants (
    user_id        uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    client_id      text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    first_login_at timestamptz NOT NULL,
    last_login_at  timestamptz NOT NULL,
    PRIMARY KEY (user_id, client_id)
);

-- A sign-out started by an app, waiting for the user to confirm on our page.
-- The redirect target is kept here, never taken from the browser, so the
-- page cannot be abused as an open redirect.
CREATE TABLE identity.oidc_logout_requests (
    id           uuid        PRIMARY KEY,
    client_id    text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    redirect_uri text        NOT NULL,
    expires_at   timestamptz NOT NULL
);

-- Logout tokens waiting for delivery to apps (outbox, like mail).
CREATE TABLE identity.oidc_logout_outbox (
    id              uuid        PRIMARY KEY,
    client_id       text        NOT NULL REFERENCES identity.oidc_clients (id) ON DELETE CASCADE,
    logout_token    text        NOT NULL,
    created_at      timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    last_error      text
);
CREATE INDEX oidc_logout_outbox_due_idx ON identity.oidc_logout_outbox (next_attempt_at);

-- +goose Down
DROP TABLE identity.oidc_logout_outbox;
DROP TABLE identity.oidc_logout_requests;
DROP TABLE identity.oidc_grants;
ALTER TABLE identity.oidc_clients DROP COLUMN backchannel_logout_uri;
ALTER TABLE identity.oidc_refresh_tokens DROP COLUMN acr, DROP COLUMN amr;
ALTER TABLE identity.oidc_auth_requests DROP COLUMN acr, DROP COLUMN amr;
ALTER TABLE identity.sessions DROP COLUMN acr, DROP COLUMN amr;
