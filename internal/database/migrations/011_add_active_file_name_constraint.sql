CREATE UNIQUE INDEX IF NOT EXISTS files_unique_active_name_idx
    ON files (
        owner_id,
        COALESCE(folder_id, '00000000-0000-0000-0000-000000000000'::uuid),
        LOWER(original_name)
    )
    WHERE deleted_at IS NULL;
