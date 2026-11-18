-- Text extraction now reads many more file types (more text and code
-- extensions, HTML and XML without tags, RTF, e-mail, every cell of a
-- spreadsheet, Word headers and notes, slide notes, e-books, archive
-- listings). Every current file is read again; the queue works through
-- them in the background.

-- +goose Up
INSERT INTO drive.index_queue (version_id, node_id)
SELECT current_version_id, id FROM drive.nodes
WHERE kind = 'file' AND current_version_id IS NOT NULL AND trashed_at IS NULL
ON CONFLICT (version_id) DO NOTHING;

-- +goose Down
SELECT 1;
