CREATE OR REPLACE VIEW active_files AS
    SELECT * FROM files WHERE deleted_at IS NULL;

CREATE OR REPLACE VIEW active_folders AS
    SELECT * FROM folders WHERE deleted_at IS NULL;
