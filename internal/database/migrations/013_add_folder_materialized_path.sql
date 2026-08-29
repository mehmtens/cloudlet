ALTER TABLE folders ADD COLUMN IF NOT EXISTS path TEXT;

WITH RECURSIVE tree AS (
    SELECT id, '/' || id::text || '/' AS calculated_path
    FROM folders
    WHERE parent_id IS NULL
    UNION ALL
    SELECT child.id, tree.calculated_path || child.id::text || '/'
    FROM folders child
    JOIN tree ON child.parent_id = tree.id
)
UPDATE folders
SET path = tree.calculated_path
FROM tree
WHERE folders.id = tree.id AND folders.path IS NULL;

ALTER TABLE folders ALTER COLUMN path SET NOT NULL;

CREATE OR REPLACE VIEW active_folders AS
    SELECT * FROM folders WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS folders_path_prefix_idx
    ON folders (path text_pattern_ops);
