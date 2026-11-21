CREATE TABLE recommendation_weak_skips (
 run_id TEXT NOT NULL REFERENCES recommendation_runs(id),
 artist_id INTEGER NOT NULL REFERENCES artists(id),
 feature_json TEXT NOT NULL CHECK(json_valid(feature_json)),
 skipped_at INTEGER NOT NULL,
 PRIMARY KEY(run_id, artist_id)
);
CREATE INDEX recommendation_weak_skips_artist_time ON recommendation_weak_skips(artist_id,skipped_at);
CREATE TABLE recommendation_decisions (
 id INTEGER PRIMARY KEY,
 artist_id INTEGER NOT NULL REFERENCES artists(id),
 action TEXT NOT NULL CHECK(action IN ('approve','reject')),
 feature_json TEXT NOT NULL CHECK(json_valid(feature_json)),
 decided_at INTEGER NOT NULL
);
CREATE TABLE recommendation_seed_hits (
 run_id TEXT NOT NULL REFERENCES recommendation_runs(id),
 artist_id INTEGER NOT NULL,
 seed_artist_id INTEGER NOT NULL,
 PRIMARY KEY(run_id,artist_id,seed_artist_id)
);
CREATE TABLE recommendation_recall_events (
 run_id TEXT NOT NULL REFERENCES recommendation_runs(id),
 artist_id INTEGER NOT NULL,
 seed_artist_id INTEGER NOT NULL,
 source TEXT NOT NULL,
 PRIMARY KEY(run_id,artist_id,seed_artist_id,source)
);
