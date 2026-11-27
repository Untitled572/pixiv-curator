ALTER TABLE library_cleanup_batch ADD COLUMN samples_json TEXT CHECK(samples_json IS NULL OR json_valid(samples_json));
