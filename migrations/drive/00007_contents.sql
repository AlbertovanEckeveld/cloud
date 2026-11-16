-- Full-text search in file contents (ADR 0017). Text extracted from each
-- file's current version, searchable with Dutch and English stemming. A
-- trigger queues every new current version for extraction, so no code path
-- can forget to; the queue is worked through in the background.

-- +goose Up
CREATE TABLE drive.contents (
    node_id    uuid        PRIMARY KEY REFERENCES drive.nodes (id) ON DELETE CASCADE,
    version_id uuid        NOT NULL,
    body       text        NOT NULL,
    tsv        tsvector    GENERATED ALWAYS AS (to_tsvector('dutch', body) || to_tsvector('english', body)) STORED,
    indexed_at timestamptz NOT NULL
);
CREATE INDEX contents_tsv_idx ON drive.contents USING gin (tsv);

CREATE TABLE drive.index_queue (
    version_id      uuid        PRIMARY KEY,
    node_id         uuid        NOT NULL REFERENCES drive.nodes (id) ON DELETE CASCADE,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX index_queue_due_idx ON drive.index_queue (next_attempt_at);

-- +goose StatementBegin
CREATE FUNCTION drive.queue_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO drive.index_queue (version_id, node_id) VALUES (NEW.current_version_id, NEW.id)
    ON CONFLICT (version_id) DO NOTHING;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER nodes_queue_index AFTER INSERT OR UPDATE OF current_version_id ON drive.nodes
    FOR EACH ROW WHEN (NEW.current_version_id IS NOT NULL) EXECUTE FUNCTION drive.queue_index();

-- Files that already exist.
INSERT INTO drive.index_queue (version_id, node_id)
SELECT current_version_id, id FROM drive.nodes WHERE kind = 'file' AND current_version_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TRIGGER nodes_queue_index ON drive.nodes;
DROP FUNCTION drive.queue_index();
DROP TABLE drive.index_queue;
DROP TABLE drive.contents;
