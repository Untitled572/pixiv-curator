CREATE TABLE jobs (
 id TEXT PRIMARY KEY, kind TEXT NOT NULL, dedupe_key TEXT NOT NULL, payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','retry_wait','paused_auth','succeeded','failed','canceled')),
 attempt INTEGER NOT NULL DEFAULT 0, available_at INTEGER NOT NULL, lease_token TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancel_requested IN (0,1)), error_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX jobs_ready ON jobs(state,available_at);
CREATE UNIQUE INDEX jobs_active_key ON jobs(dedupe_key) WHERE state IN ('pending','running','retry_wait','paused_auth');
