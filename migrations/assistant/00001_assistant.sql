-- The assistant (ADR 0023): AI providers configured in the web app, an
-- index of embeddings over files' text, and conversations.
--
-- pgvector is not a trusted extension: a superuser installs it once in the
-- database (see deploy/README.md). IF NOT EXISTS then succeeds for the
-- platform's own role.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

-- An OpenAI-compatible endpoint. external is decided by the platform from
-- the address (outside the home and NetBird networks); the API key is
-- sealed with the platform key, bound to the row, and never returned.
CREATE TABLE assistant.providers (
    id                 uuid        PRIMARY KEY,
    name               text        NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 60),
    base_url           text        NOT NULL CHECK (char_length(base_url) <= 500),
    api_key            bytea,
    external           boolean     NOT NULL,
    -- For an external provider: who agreed that content goes there, and when.
    consent_by         uuid,
    consent_at         timestamptz,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    CHECK (NOT external OR consent_at IS NOT NULL)
);

-- Which provider and model answers questions, and which makes embeddings.
CREATE TABLE assistant.settings (
    singleton      boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
    chat_provider  uuid        REFERENCES assistant.providers (id) ON DELETE SET NULL,
    chat_model     text        CHECK (char_length(chat_model) <= 200),
    embed_provider uuid        REFERENCES assistant.providers (id) ON DELETE SET NULL,
    embed_model    text        CHECK (char_length(embed_model) <= 200),
    updated_at     timestamptz NOT NULL
);
INSERT INTO assistant.settings (updated_at) VALUES (now());

-- Per file: which version and model it was indexed with. The text itself
-- stays in drive; chunks are only positions in it.
CREATE TABLE assistant.documents (
    node_id    uuid        PRIMARY KEY,
    version_id uuid        NOT NULL,
    model      text        NOT NULL,
    chunks     integer     NOT NULL,
    indexed_at timestamptz NOT NULL,
    error      text
);

CREATE TABLE assistant.chunks (
    node_id   uuid    NOT NULL,
    model     text    NOT NULL,
    chunk_no  integer NOT NULL,
    start_at  integer NOT NULL CHECK (start_at >= 0),
    end_at    integer NOT NULL CHECK (end_at > start_at),
    embedding vector  NOT NULL,
    PRIMARY KEY (node_id, model, chunk_no)
);
CREATE INDEX chunks_model_idx ON assistant.chunks (model);

-- A user's conversations; nobody else, administrators included, sees them.
CREATE TABLE assistant.conversations (
    id         uuid        PRIMARY KEY,
    user_id    uuid        NOT NULL,
    title      text        NOT NULL CHECK (char_length(title) <= 200),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
CREATE INDEX conversations_user_idx ON assistant.conversations (user_id, updated_at DESC);

CREATE TABLE assistant.messages (
    id              uuid        PRIMARY KEY,
    conversation_id uuid        NOT NULL REFERENCES assistant.conversations (id) ON DELETE CASCADE,
    role            text        NOT NULL CHECK (role IN ('user', 'assistant')),
    content         text        NOT NULL,
    sources         jsonb       NOT NULL DEFAULT '[]',
    created_at      timestamptz NOT NULL
);
CREATE INDEX messages_conversation_idx ON assistant.messages (conversation_id, created_at);

-- +goose Down
DROP TABLE assistant.messages;
DROP TABLE assistant.conversations;
DROP TABLE assistant.chunks;
DROP TABLE assistant.documents;
DROP TABLE assistant.settings;
DROP TABLE assistant.providers;
