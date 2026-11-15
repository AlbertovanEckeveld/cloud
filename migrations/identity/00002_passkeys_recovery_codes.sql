-- +goose Up

-- When the user last proved their identity in this session (password + second
-- factor, or a passkey). Sensitive changes require this to be recent.
ALTER TABLE identity.sessions ADD COLUMN authenticated_at timestamptz;
UPDATE identity.sessions SET authenticated_at = created_at WHERE state = 'authenticated';

-- WebAuthn credentials (passkeys). credential holds the library's credential
-- record as JSON: public key, sign counter, flags, transports. A public key
-- is not secret, so it is stored unencrypted.
CREATE TABLE identity.passkeys (
    id            uuid        PRIMARY KEY,
    user_id       uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    credential_id bytea       NOT NULL UNIQUE,
    name          text        NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    credential    jsonb       NOT NULL,
    created_at    timestamptz NOT NULL,
    last_used_at  timestamptz
);
CREATE INDEX passkeys_user_id_idx ON identity.passkeys (user_id);

-- Single-use recovery codes, stored as SHA-256 (they carry 80 random bits).
CREATE TABLE identity.recovery_codes (
    user_id    uuid        NOT NULL REFERENCES identity.users (id) ON DELETE CASCADE,
    code_hash  bytea       NOT NULL CHECK (length(code_hash) = 32),
    created_at timestamptz NOT NULL,
    used_at    timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- Pending WebAuthn ceremonies. A challenge is used at most once and expires
-- after a few minutes. Kept in the database so any instance can finish a
-- ceremony another instance began.
CREATE TABLE identity.webauthn_challenges (
    id           uuid        PRIMARY KEY,
    purpose      text        NOT NULL CHECK (purpose IN ('register', 'login')),
    user_id      uuid        REFERENCES identity.users (id) ON DELETE CASCADE,
    session_id   uuid        REFERENCES identity.sessions (id) ON DELETE CASCADE,
    session_data jsonb       NOT NULL,
    expires_at   timestamptz NOT NULL
);

-- +goose Down
DROP TABLE identity.webauthn_challenges;
DROP TABLE identity.recovery_codes;
DROP TABLE identity.passkeys;
ALTER TABLE identity.sessions DROP COLUMN authenticated_at;
