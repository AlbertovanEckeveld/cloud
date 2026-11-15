-- +goose Up

-- Accounts. password_hash is an Argon2id PHC string; NULL until the user has
-- activated the account through an activation link.
CREATE TABLE identity.users (
    id            uuid        PRIMARY KEY,
    username      text        NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,31}$'),
    display_name  text        NOT NULL CHECK (length(display_name) BETWEEN 1 AND 100),
    is_admin      boolean     NOT NULL DEFAULT false,
    password_hash text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- TOTP second factor. The secret is encrypted with the platform key (AES-GCM,
-- bound to the user id). confirmed_at is NULL while enrollment is pending.
-- last_used_step blocks replay of a code within its validity window.
CREATE TABLE identity.totp_credentials (
    user_id        uuid        PRIMARY KEY REFERENCES identity.users (id) ON DELETE CASCADE,
    secret_sealed  bytea       NOT NULL,
    confirmed_at   timestamptz,
    last_used_step bigint      NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One-time links to set a password and enrol a second factor. Only the
-- SHA-256 of the token is stored.
CREATE TABLE identity.activation_tokens (
    token_hash bytea       PRIMARY KEY CHECK (length(token_hash) = 32),
    user_id    uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX activation_tokens_user_id_idx ON identity.activation_tokens (user_id);

-- Browser sessions. Only the SHA-256 of the cookie token is stored, so a
-- database leak does not yield usable sessions. A session is "partial" until
-- the second factor is verified (or enrolled, after activation).
CREATE TABLE identity.sessions (
    id                    uuid        PRIMARY KEY,
    token_hash            bytea       NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    user_id               uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    state                 text        NOT NULL CHECK (state IN ('mfa_required', 'mfa_enrollment_required', 'authenticated')),
    activation_token_hash bytea,
    site_id               text        NOT NULL,
    ip                    text        NOT NULL,
    user_agent            text        NOT NULL,
    created_at            timestamptz NOT NULL,
    last_seen_at          timestamptz NOT NULL,
    expires_at            timestamptz NOT NULL
);
CREATE INDEX sessions_user_id_idx ON identity.sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON identity.sessions (expires_at);

-- Failed-attempt counters for login throttling, keyed by e.g. "user:<name>"
-- or "ip:<address>". Stored in the database so all instances share them.
CREATE TABLE identity.throttle (
    key             text        PRIMARY KEY,
    failures        integer     NOT NULL,
    last_failure_at timestamptz NOT NULL,
    locked_until    timestamptz
);

-- Append-only security log. No foreign keys: history must survive deletions.
CREATE TABLE identity.audit_events (
    id          uuid        PRIMARY KEY,
    occurred_at timestamptz NOT NULL,
    site_id     text        NOT NULL,
    event       text        NOT NULL,
    actor_id    uuid,
    subject_id  uuid,
    ip          text        NOT NULL DEFAULT '',
    user_agent  text        NOT NULL DEFAULT '',
    details     jsonb       NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_events_actor_idx ON identity.audit_events (actor_id, occurred_at DESC);
CREATE INDEX audit_events_subject_idx ON identity.audit_events (subject_id, occurred_at DESC);

-- +goose Down
DROP TABLE identity.audit_events;
DROP TABLE identity.throttle;
DROP TABLE identity.sessions;
DROP TABLE identity.activation_tokens;
DROP TABLE identity.totp_credentials;
DROP TABLE identity.users;
