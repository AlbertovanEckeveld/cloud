-- Activity (ADR 0018): the change log also records what happened, by whom
-- and through which channel (web, desktop, webdav, editor), so users can see
-- what is being synchronised. The channel is set by the code that changes a
-- node; the trigger derives the action from the old and new row.

-- +goose Up
ALTER TABLE drive.nodes ADD COLUMN updated_via text;
ALTER TABLE drive.changes
    ADD COLUMN action text,
    ADD COLUMN actor  uuid,
    ADD COLUMN via    text,
    ADD COLUMN name   text;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION drive.record_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    act text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        act := 'created';
    ELSIF TG_OP = 'DELETE' THEN
        act := 'deleted';
    ELSIF OLD.trashed_at IS NULL AND NEW.trashed_at IS NOT NULL THEN
        act := 'trashed';
    ELSIF OLD.trashed_at IS NOT NULL AND NEW.trashed_at IS NULL THEN
        act := 'restored';
    ELSIF OLD.current_version_id IS DISTINCT FROM NEW.current_version_id THEN
        act := 'updated';
    ELSIF OLD.parent_id IS DISTINCT FROM NEW.parent_id THEN
        act := 'moved';
    ELSIF OLD.name <> NEW.name THEN
        act := 'renamed';
    ELSE
        act := 'changed';
    END IF;
    INSERT INTO drive.changes (node_id, owner_id, action, actor, via, name)
    VALUES (COALESCE(NEW.id, OLD.id), COALESCE(NEW.owner_id, OLD.owner_id), act,
            CASE WHEN TG_OP = 'DELETE' THEN NULL ELSE NEW.updated_by END,
            CASE WHEN TG_OP = 'DELETE' THEN NULL ELSE NEW.updated_via END,
            COALESCE(NEW.name, OLD.name));
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION drive.record_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO drive.changes (node_id, owner_id) VALUES (COALESCE(NEW.id, OLD.id), COALESCE(NEW.owner_id, OLD.owner_id));
    RETURN NULL;
END
$$;
-- +goose StatementEnd
ALTER TABLE drive.changes DROP COLUMN action, DROP COLUMN actor, DROP COLUMN via, DROP COLUMN name;
ALTER TABLE drive.nodes DROP COLUMN updated_via;
