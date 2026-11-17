-- Change log for synchronisation (ADR 0016). A trigger records every
-- insert, update and delete of a node, so no code path can forget to. Sync
-- clients read it with a cursor of (transaction ID, change ID) and only see
-- changes of transactions that have finished (below the snapshot's xmin):
-- a transaction that started earlier but commits later is never skipped.

-- +goose Up
-- UUIDv7 (RFC 9562) in SQL: PostgreSQL 17 has no built-in; IDs must be
-- UUIDv7 everywhere (CLAUDE.md), never sequences.
-- +goose StatementBegin
CREATE FUNCTION drive.uuidv7() RETURNS uuid LANGUAGE sql VOLATILE AS $$
    SELECT encode(
        set_bit(set_bit(
            overlay(uuid_send(gen_random_uuid())
                    PLACING substring(int8send((extract(epoch FROM clock_timestamp()) * 1000)::bigint) FROM 3)
                    FROM 1 FOR 6),
            52, 1), 53, 1),
        'hex')::uuid
$$;
-- +goose StatementEnd

CREATE TABLE drive.changes (
    txid     xid8        NOT NULL DEFAULT pg_current_xact_id(),
    id       uuid        NOT NULL DEFAULT drive.uuidv7(),
    node_id  uuid        NOT NULL,
    -- The tree's owner (user or team folder): clients only read changes of
    -- the trees they sync. Nodes never change owner.
    owner_id uuid        NOT NULL,
    at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (txid, id)
);
CREATE INDEX changes_owner_idx ON drive.changes (owner_id, txid, id);
CREATE INDEX changes_at_idx ON drive.changes (at);

-- +goose StatementBegin
CREATE FUNCTION drive.record_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO drive.changes (node_id, owner_id) VALUES (COALESCE(NEW.id, OLD.id), COALESCE(NEW.owner_id, OLD.owner_id));
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER nodes_changes AFTER INSERT OR UPDATE OR DELETE ON drive.nodes
    FOR EACH ROW EXECUTE FUNCTION drive.record_change();

-- +goose Down
DROP TRIGGER nodes_changes ON drive.nodes;
DROP FUNCTION drive.record_change();
DROP TABLE drive.changes;
DROP FUNCTION drive.uuidv7();
