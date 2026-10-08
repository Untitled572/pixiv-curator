CREATE TABLE artists (
 id INTEGER PRIMARY KEY CHECK(id>0), name TEXT NOT NULL DEFAULT '', account TEXT NOT NULL DEFAULT '', avatar_url TEXT NOT NULL DEFAULT '',
 review_status TEXT NOT NULL DEFAULT 'pending' CHECK(review_status IN ('pending','approved','rejected','blocked')),
 collection_enabled INTEGER NOT NULL DEFAULT 0 CHECK(collection_enabled IN (0,1)), remote_followed INTEGER NOT NULL DEFAULT 0 CHECK(remote_followed IN (0,1)),
 last_seen_follow_run TEXT NOT NULL DEFAULT '', last_synced_at INTEGER,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX artists_review ON artists(review_status,collection_enabled);
CREATE TABLE artist_sources (artist_id INTEGER NOT NULL REFERENCES artists(id), source_type TEXT NOT NULL CHECK(source_type IN ('following','manual','recommend')), source_key TEXT NOT NULL, first_seen_at INTEGER NOT NULL, PRIMARY KEY(artist_id,source_type,source_key));
CREATE TABLE artworks (
 id INTEGER PRIMARY KEY CHECK(id>0), artist_id INTEGER NOT NULL REFERENCES artists(id), title TEXT NOT NULL DEFAULT '', caption TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL DEFAULT 'unknown' CHECK(kind IN ('illust','manga','ugoira','unknown')),
 page_count INTEGER NOT NULL CHECK(page_count>=0), rating TEXT NOT NULL DEFAULT 'unknown' CHECK(rating IN ('safe','r18','r18g','unknown')),
 ai_status TEXT NOT NULL DEFAULT 'unknown' CHECK(ai_status IN ('non_ai','ai','unknown')), bookmarks INTEGER, views INTEGER, published_at INTEGER,
 metadata_state TEXT NOT NULL DEFAULT 'pending' CHECK(metadata_state IN ('pending','complete','unavailable')),
 visible INTEGER NOT NULL DEFAULT 1 CHECK(visible IN (0,1)), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX artworks_artist ON artworks(artist_id,published_at,id);
CREATE INDEX artworks_filter ON artworks(visible,metadata_state,rating,ai_status);
CREATE TABLE image_pages (
 illust_id INTEGER NOT NULL REFERENCES artworks(id), page_index INTEGER NOT NULL CHECK(page_index>=0), original_url TEXT NOT NULL DEFAULT '', preview_url TEXT NOT NULL DEFAULT '',
 width INTEGER NOT NULL CHECK(width>=0), height INTEGER NOT NULL CHECK(height>=0), media_state TEXT NOT NULL CHECK(media_state IN ('indexed','unavailable')),
 failure_count INTEGER NOT NULL DEFAULT 0, last_ok_at INTEGER, last_error_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 PRIMARY KEY(illust_id,page_index)
);
CREATE TABLE tags (id INTEGER PRIMARY KEY, normalized_name TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL);
CREATE TABLE artwork_tags (illust_id INTEGER NOT NULL REFERENCES artworks(id), tag_id INTEGER NOT NULL REFERENCES tags(id), PRIMARY KEY(illust_id,tag_id));
CREATE INDEX artwork_tags_tag ON artwork_tags(tag_id,illust_id);
CREATE TABLE artist_sync_state (artist_id INTEGER PRIMARY KEY REFERENCES artists(id), phase TEXT NOT NULL, cursor TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL, last_full_sync_at INTEGER, last_incremental_at INTEGER);
CREATE TABLE settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL CHECK(json_valid(value_json)), version INTEGER NOT NULL CHECK(version>0), updated_at INTEGER NOT NULL);
