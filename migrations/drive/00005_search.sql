-- Fast search on (parts of) names: a trigram index makes "%rapp%" use an
-- index instead of reading every row. pg_trgm ships with PostgreSQL and is a
-- trusted extension, so the database owner may install it.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE INDEX nodes_name_trgm_idx ON drive.nodes USING gin (lower(name) public.gin_trgm_ops) WHERE trashed_at IS NULL;

-- +goose Down
DROP INDEX drive.nodes_name_trgm_idx;
