CREATE TABLE recommendation_runs (
 id TEXT PRIMARY KEY, state TEXT NOT NULL, input_version TEXT NOT NULL,
 budget_json TEXT NOT NULL CHECK(json_valid(budget_json)), started_at INTEGER NOT NULL, finished_at INTEGER,
 stats_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(stats_json)), seed_cursor INTEGER NOT NULL DEFAULT 0,
 candidate_cursor INTEGER NOT NULL DEFAULT 0, request_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE recommendations (
 run_id TEXT NOT NULL REFERENCES recommendation_runs(id), artist_id INTEGER NOT NULL REFERENCES artists(id),
 score REAL NOT NULL CHECK(score>=0 AND score<=1), reasons_json TEXT NOT NULL CHECK(json_valid(reasons_json)),
 sample_illust_ids_json TEXT NOT NULL CHECK(json_valid(sample_illust_ids_json)),
 sources_json TEXT NOT NULL CHECK(json_valid(sources_json)), confidence TEXT NOT NULL DEFAULT 'low',
 score_version TEXT NOT NULL DEFAULT '', components_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(components_json)),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(run_id,artist_id)
);
CREATE INDEX recommendations_artist ON recommendations(artist_id,created_at);
