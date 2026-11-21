ALTER TABLE distribution_history ADD COLUMN filter_preset TEXT;
ALTER TABLE distribution_history ADD COLUMN delivery_mode TEXT CHECK (delivery_mode IN ('proxy','original'));
ALTER TABLE distribution_history ADD COLUMN delivered_at INTEGER;
CREATE TABLE exposure_work (
 illust_id INTEGER PRIMARY KEY REFERENCES artworks(id) ON DELETE CASCADE,
 exposed_at INTEGER NOT NULL
);
CREATE TABLE exposure_artist (
 artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
 orientation TEXT NOT NULL CHECK (orientation IN ('any','portrait','landscape','square')),
 exposed_at INTEGER NOT NULL,
 PRIMARY KEY(artist_id,orientation)
);
