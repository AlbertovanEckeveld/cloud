-- Who last changed a file or folder, shown in shared folders and team folders.

-- +goose Up
ALTER TABLE drive.nodes ADD COLUMN updated_by uuid;
-- Existing rows: the uploader of the current version, else the owner.
UPDATE drive.nodes n SET updated_by = COALESCE(
    (SELECT v.created_by FROM drive.versions v WHERE v.id = n.current_version_id), n.owner_id);

-- +goose Down
ALTER TABLE drive.nodes DROP COLUMN updated_by;
