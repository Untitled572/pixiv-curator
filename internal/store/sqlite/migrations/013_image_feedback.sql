CREATE TABLE image_feedback (
 illust_id INTEGER NOT NULL REFERENCES artworks(id),
 page_index INTEGER NOT NULL CHECK(page_index>=0),
 value TEXT NOT NULL CHECK(value IN ('like','dislike')),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(illust_id,page_index)
);
CREATE INDEX image_feedback_value ON image_feedback(value,illust_id,page_index);
