CREATE TABLE remembered_sessions (
 session_hash BLOB PRIMARY KEY NOT NULL CHECK(length(session_hash)=32),
 csrf_hash BLOB NOT NULL CHECK(length(csrf_hash)=32),
 expires_at INTEGER NOT NULL
);
