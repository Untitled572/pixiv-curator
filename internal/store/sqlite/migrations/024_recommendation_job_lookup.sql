CREATE INDEX jobs_recommendation_run
 ON jobs(json_extract(payload_json,'$.run_id'), created_at DESC)
 WHERE kind='recommend_artists';
