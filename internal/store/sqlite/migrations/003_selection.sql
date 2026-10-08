CREATE TABLE artwork_preferences (illust_id INTEGER PRIMARY KEY REFERENCES artworks(id), value TEXT NOT NULL CHECK(value IN ('like','dislike','neutral')), updated_at INTEGER NOT NULL);
CREATE TABLE feedback_events (id TEXT PRIMARY KEY, external_event_id TEXT UNIQUE, illust_id INTEGER NOT NULL REFERENCES artworks(id), action TEXT NOT NULL CHECK(action IN ('like','dislike','neutral')), source TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE selection_history (id TEXT PRIMARY KEY, collection TEXT NOT NULL, illust_id INTEGER NOT NULL, page_index INTEGER NOT NULL, selected_at INTEGER NOT NULL, request_id TEXT NOT NULL, FOREIGN KEY(illust_id,page_index) REFERENCES image_pages(illust_id,page_index));
CREATE INDEX selection_recent ON selection_history(collection,selected_at);
CREATE INDEX selection_page ON selection_history(illust_id,page_index,selected_at);
