-- Version history details: how a version arrived (as nodes.updated_via),
-- and an optional name the user gives a version ("ingeleverd"). A named
-- version is kept when old versions are cleaned up.

-- +goose Up
ALTER TABLE drive.versions
    ADD COLUMN created_via text CHECK (created_via IN ('web', 'desktop', 'webdav', 'editor')),
    ADD COLUMN label       text CHECK (char_length(label) BETWEEN 1 AND 80);

-- +goose Down
ALTER TABLE drive.versions DROP COLUMN label, DROP COLUMN created_via;
