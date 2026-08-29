DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='shares' AND column_name='max_downloads') THEN
        ALTER TABLE shares RENAME COLUMN max_downloads TO max_access_starts;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='shares' AND column_name='download_count') THEN
        ALTER TABLE shares RENAME COLUMN download_count TO access_start_count;
    END IF;
END $$;
