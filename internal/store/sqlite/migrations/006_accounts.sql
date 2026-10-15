CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT NOT NULL, pixiv_id INTEGER NOT NULL DEFAULT 0, token_file TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)), created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX accounts_pixiv ON accounts(pixiv_id) WHERE pixiv_id>0 AND deleted=0;
CREATE TABLE account_artists (account_id TEXT NOT NULL REFERENCES accounts(id), artist_id INTEGER NOT NULL REFERENCES artists(id), followed INTEGER NOT NULL DEFAULT 1 CHECK(followed IN (0,1)), last_seen_run TEXT NOT NULL DEFAULT '', PRIMARY KEY(account_id,artist_id));
CREATE INDEX account_artists_artist ON account_artists(artist_id,account_id);
