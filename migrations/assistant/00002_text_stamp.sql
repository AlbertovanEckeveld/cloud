-- When drive read a file's text again (for example after it learned new
-- file types), the version stays the same but the text changes: the index
-- remembers which reading it used and indexes again when it is newer.

-- +goose Up
ALTER TABLE assistant.documents ADD COLUMN text_at timestamptz;

-- +goose Down
ALTER TABLE assistant.documents DROP COLUMN text_at;
