CREATE TABLE artist_tag_overrides (
 artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE,
 tags_json TEXT NOT NULL CHECK(json_valid(tags_json)),
 updated_at INTEGER NOT NULL
);
