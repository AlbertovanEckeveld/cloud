-- Outgoing mail waiting for delivery (outbox pattern). A message is inserted
-- in the same transaction as the action that causes it, and deleted once
-- sent or after too many failures. The body can contain secrets (activation
-- links), so it is encrypted with the platform key.

-- +goose Up
CREATE TABLE core.mail_outbox (
    id              uuid        PRIMARY KEY,
    recipient       text        NOT NULL,
    subject         text        NOT NULL,
    body_sealed     bytea       NOT NULL,
    created_at      timestamptz NOT NULL,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    last_error      text
);
CREATE INDEX mail_outbox_due_idx ON core.mail_outbox (next_attempt_at);

-- +goose Down
DROP TABLE core.mail_outbox;
