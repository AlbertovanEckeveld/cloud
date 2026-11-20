-- One fixed value sealed with ENCRYPTION_KEY when the database is first set
-- up. At every start the platform opens it, and refuses to run if that
-- fails: a different key would otherwise silently mix data sealed under two
-- keys (keycheck package). All sites share the key, so one row serves all.

-- +goose Up
CREATE TABLE core.key_canary (
    singleton  boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    sealed     bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE core.key_canary;
