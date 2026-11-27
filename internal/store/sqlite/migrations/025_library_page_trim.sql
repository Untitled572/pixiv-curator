ALTER TABLE artworks ADD COLUMN retained_page_indices TEXT CHECK(json_valid(retained_page_indices) OR retained_page_indices IS NULL);
ALTER TABLE library_cleanup_batch ADD COLUMN trimmed INTEGER NOT NULL DEFAULT 0 CHECK(trimmed IN (0,1));
