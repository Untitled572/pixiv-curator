ALTER TABLE jobs ADD COLUMN progress_pages INTEGER NOT NULL DEFAULT 0 CHECK(progress_pages >= 0);
ALTER TABLE jobs ADD COLUMN progress_items INTEGER NOT NULL DEFAULT 0 CHECK(progress_items >= 0);
