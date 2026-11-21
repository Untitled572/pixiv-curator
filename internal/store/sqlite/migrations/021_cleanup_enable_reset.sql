CREATE TRIGGER cleanup_reset_on_collection_enable
AFTER UPDATE OF collection_enabled ON artists
WHEN OLD.collection_enabled=0 AND NEW.collection_enabled=1 AND NEW.cleanup_enabled_at IS NOT NULL
BEGIN
 DELETE FROM library_cleanup_skip WHERE artist_id=NEW.id;
END;
