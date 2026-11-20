-- What the assistant did for an answer (searched, read, listed), shown with
-- the answer (ADR 0024).

-- +goose Up
ALTER TABLE assistant.messages ADD COLUMN steps jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE assistant.messages DROP COLUMN steps;
