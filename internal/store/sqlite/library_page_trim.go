package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// TrimLibraryArtistPages removes pagination rows without changing discovery decisions or artwork metadata.
func (s *Store) TrimLibraryArtistPages(ctx context.Context, id, round int64) (int64, int64, error) {
	if id <= 0 || round <= 0 {
		return 0, 0, domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE library_cleanup_batch SET trimmed=1
WHERE artist_id=? AND trimmed=0
AND EXISTS(SELECT 1 FROM library_cleanup_state WHERE id=1 AND initialized=1 AND round=?)
AND EXISTS(SELECT 1 FROM artists WHERE id=library_cleanup_batch.artist_id AND review_status='approved' AND collection_enabled=1)
AND NOT EXISTS(SELECT 1 FROM jobs WHERE dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth'))`, id, round, fmt.Sprintf("artist-restore:%d", id))
	if err != nil {
		return 0, 0, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return 0, 0, err
		}
		return 0, 0, domain.ErrConflict
	}

	var total, first int64
	err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE p.page_index=0)
FROM artworks w JOIN image_pages p ON p.illust_id=w.id WHERE w.artist_id=? AND w.kind<>'manga'`, id).Scan(&total, &first)
	if err != nil {
		return 0, 0, err
	}
	target := max(total/2, first)
	if total <= target || total < 2 {
		return total, total, nil
	}

	_, err = tx.ExecContext(ctx, `WITH ranked AS (
 SELECT p.illust_id,p.page_index,
  row_number() OVER (PARTITION BY p.page_index=0 ORDER BY CASE WHEN p.page_index>0 THEN random() END) AS rank
 FROM artworks w JOIN image_pages p ON p.illust_id=w.id WHERE w.artist_id=? AND w.kind<>'manga'
), keep AS (
 SELECT illust_id,page_index,(page_index=0 OR rank<=?) AS retained FROM ranked
), limits AS MATERIALIZED (
 SELECT illust_id,json_group_array(page_index ORDER BY page_index) FILTER (WHERE retained) AS page_indices
 FROM keep GROUP BY illust_id HAVING min(retained)=0
)
UPDATE artworks SET retained_page_indices=limits.page_indices FROM limits WHERE artworks.id=limits.illust_id`, id, target-first)
	if err != nil {
		return 0, 0, err
	}
	// History rows reference the exact page. Keep independent feedback and work metadata
	// for discovery, while removing the history references that would outlive a page.
	pruned := `(illust_id,page_index) IN (
 SELECT p.illust_id,p.page_index FROM artworks w JOIN image_pages p ON p.illust_id=w.id
 WHERE w.artist_id=? AND w.kind<>'manga' AND w.retained_page_indices IS NOT NULL
 AND p.page_index NOT IN (SELECT value FROM json_each(w.retained_page_indices))
)`
	for _, table := range []string{"selection_history", "distribution_history", "image_pages"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+pruned, id); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	s.RequestSelectionSnapshotRefresh()
	return total, target, nil
}

// RestoreLibraryArtistPages removes the trim limits and queues a full artwork
// scan from the start at history priority in the same transaction.
func (s *Store) RestoreLibraryArtistPages(ctx context.Context, id int64) (string, error) {
	if id <= 0 {
		return "", domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE artworks SET retained_page_indices=NULL
WHERE artist_id=? AND kind<>'manga' AND retained_page_indices IS NOT NULL
AND EXISTS(SELECT 1 FROM artists WHERE id=artworks.artist_id AND review_status='approved' AND collection_enabled=1)`, id)
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", domain.ErrConflict
	}
	payload, _ := json.Marshal(map[string]any{"artist_id": id, "phase": "history"})
	jobID, err := enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist-restore:%d", id), payload)
	if err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}
