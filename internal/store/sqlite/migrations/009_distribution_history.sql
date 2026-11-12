CREATE TABLE distribution_history (
 id TEXT PRIMARY KEY,
 collection TEXT NOT NULL,
 illust_id INTEGER NOT NULL,
 page_index INTEGER NOT NULL,
 selected_at INTEGER NOT NULL,
 request_id TEXT NOT NULL,
 FOREIGN KEY(illust_id,page_index) REFERENCES image_pages(illust_id,page_index)
);
CREATE INDEX distribution_history_recent ON distribution_history(collection,selected_at,id);
INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id)
 SELECT id,collection,illust_id,page_index,selected_at,request_id FROM selection_history;
