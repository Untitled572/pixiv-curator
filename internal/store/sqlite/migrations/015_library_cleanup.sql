CREATE TABLE library_cleanup_state (id INTEGER PRIMARY KEY CHECK(id=1), round INTEGER NOT NULL CHECK(round>0), initialized INTEGER NOT NULL CHECK(initialized IN (0,1)));
INSERT INTO library_cleanup_state VALUES(1,1,0);
CREATE TABLE library_cleanup_batch (artist_id INTEGER PRIMARY KEY REFERENCES artists(id), position INTEGER NOT NULL, kept INTEGER NOT NULL DEFAULT 0 CHECK(kept IN (0,1)));
CREATE TABLE library_cleanup_skip (artist_id INTEGER PRIMARY KEY REFERENCES artists(id), until_round INTEGER NOT NULL);
