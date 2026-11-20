-- The index covers sources (ADR 0024): drive now, later pim, photos, mail.
-- An item is (source, item_id); its stamp changes when its text does. The
-- index is derived data, so it is rebuilt rather than converted. Sources of
-- earlier answers had drive's shape and are cleared; the answers remain.

-- +goose Up
DROP TABLE assistant.chunks;
DROP TABLE assistant.documents;

CREATE TABLE assistant.documents (
    source     text        NOT NULL,
    item_id    text        NOT NULL,
    stamp      text        NOT NULL,
    model      text        NOT NULL,
    chunks     integer     NOT NULL,
    indexed_at timestamptz NOT NULL,
    error      text,
    PRIMARY KEY (source, item_id)
);

CREATE TABLE assistant.chunks (
    source    text    NOT NULL,
    item_id   text    NOT NULL,
    model     text    NOT NULL,
    chunk_no  integer NOT NULL,
    start_at  integer NOT NULL CHECK (start_at >= 0),
    end_at    integer NOT NULL CHECK (end_at > start_at),
    embedding vector  NOT NULL,
    PRIMARY KEY (source, item_id, model, chunk_no)
);
CREATE INDEX chunks_model_idx ON assistant.chunks (model);

UPDATE assistant.messages SET sources = '[]' WHERE sources <> '[]';

-- +goose Down
DROP TABLE assistant.chunks;
DROP TABLE assistant.documents;
CREATE TABLE assistant.documents (
    node_id    uuid        PRIMARY KEY,
    version_id uuid        NOT NULL,
    model      text        NOT NULL,
    chunks     integer     NOT NULL,
    indexed_at timestamptz NOT NULL,
    error      text,
    text_at    timestamptz
);
CREATE TABLE assistant.chunks (
    node_id   uuid    NOT NULL,
    model     text    NOT NULL,
    chunk_no  integer NOT NULL,
    start_at  integer NOT NULL,
    end_at    integer NOT NULL,
    embedding vector  NOT NULL,
    PRIMARY KEY (node_id, model, chunk_no)
);
