CREATE INDEX jobs_recent_success ON jobs(dedupe_key,updated_at) WHERE state='succeeded';
