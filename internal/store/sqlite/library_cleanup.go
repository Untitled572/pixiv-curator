package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// LibraryCleanupReview returns the persisted current batch without advancing it.
func (s *Store) LibraryCleanupReview(ctx context.Context) (int64, []domain.LibraryCleanupItem, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	defer tx.Rollback()
	var round int64
	var initialized bool
	if err = tx.QueryRowContext(ctx, `SELECT round,initialized FROM library_cleanup_state WHERE id=1`).Scan(&round, &initialized); err != nil {
		return 0, nil, nil, err
	}
	if !initialized {
		if err = fillCleanupBatch(ctx, tx, round); err != nil {
			return 0, nil, nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE library_cleanup_state SET initialized=1 WHERE id=1`); err != nil {
			return 0, nil, nil, err
		}
	}
	items, kept, err := cleanupItems(ctx, tx)
	if err != nil {
		return 0, nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, nil, err
	}
	return round, items, kept, nil
}

func fillCleanupBatch(ctx context.Context, tx *sql.Tx, round int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO library_cleanup_batch(artist_id,position) SELECT id, row_number() OVER () FROM (SELECT a.id FROM artists a LEFT JOIN library_cleanup_skip s ON s.artist_id=a.id WHERE a.review_status='approved' AND a.collection_enabled=1 AND COALESCE(s.until_round,0)<? ORDER BY EXISTS(SELECT 1 FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 WHERE w.artist_id=a.id AND w.visible=1 AND p.media_state='indexed') DESC, random() LIMIT 6)`, round)
	return err
}

func cleanupItems(ctx context.Context, tx *sql.Tx) ([]domain.LibraryCleanupItem, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed,b.kept FROM library_cleanup_batch b JOIN artists a ON a.id=b.artist_id WHERE a.review_status='approved' AND a.collection_enabled=1 ORDER BY b.position`)
	if err != nil {
		return nil, nil, err
	}
	items := []domain.LibraryCleanupItem{}
	kept := []string{}
	for rows.Next() {
		var item domain.LibraryCleanupItem
		var isKept bool
		if err = rows.Scan(&item.Artist.ID, &item.Artist.Name, &item.Artist.Account, &item.Artist.AvatarURL, &item.Artist.ReviewStatus, &item.Artist.CollectionEnabled, &item.Artist.RemoteFollowed, &isKept); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if isKept {
			kept = append(kept, fmt.Sprint(item.Artist.ID))
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for i := range items {
		rows, err = tx.QueryContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 WHERE w.artist_id=? AND w.visible=1 AND p.media_state='indexed' ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC LIMIT 3`, items[i].Artist.ID)
		if err != nil {
			return nil, nil, err
		}
		items[i].SampleIllustIDs = []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			items[i].SampleIllustIDs = append(items[i].SampleIllustIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return items, kept, nil
}

// NextLibraryCleanup advances only from the caller's current round.
func (s *Store) NextLibraryCleanup(ctx context.Context, expected int64) (int64, []domain.LibraryCleanupItem, []string, error) {
	if expected <= 0 {
		return 0, nil, nil, domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	defer tx.Rollback()
	var round int64
	var initialized bool
	if err = tx.QueryRowContext(ctx, `SELECT round,initialized FROM library_cleanup_state WHERE id=1`).Scan(&round, &initialized); err != nil {
		return 0, nil, nil, err
	}
	if !initialized || round != expected {
		return 0, nil, nil, domain.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO library_cleanup_skip(artist_id,until_round) SELECT b.artist_id,? FROM library_cleanup_batch b JOIN artists a ON a.id=b.artist_id WHERE a.review_status='approved' AND a.collection_enabled=1 ON CONFLICT(artist_id) DO UPDATE SET until_round=MAX(until_round,excluded.until_round)`, round+50); err != nil {
		return 0, nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM library_cleanup_batch`); err != nil {
		return 0, nil, nil, err
	}
	round++
	if _, err = tx.ExecContext(ctx, `UPDATE library_cleanup_state SET round=? WHERE id=1`, round); err != nil {
		return 0, nil, nil, err
	}
	if err = fillCleanupBatch(ctx, tx, round); err != nil {
		return 0, nil, nil, err
	}
	items, kept, err := cleanupItems(ctx, tx)
	if err != nil {
		return 0, nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, nil, err
	}
	return round, items, kept, nil
}

// KeepLibraryCleanupArtist records an explicit decision for the current batch.
func (s *Store) KeepLibraryCleanupArtist(ctx context.Context, id, expected int64) error {
	if id <= 0 || expected <= 0 {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var round int64
	if err = tx.QueryRowContext(ctx, `SELECT round FROM library_cleanup_state WHERE id=1 AND initialized=1`).Scan(&round); err == sql.ErrNoRows {
		return domain.ErrConflict
	} else if err != nil {
		return err
	}
	if round != expected {
		return domain.ErrConflict
	}
	r, err := tx.ExecContext(ctx, `UPDATE library_cleanup_batch SET kept=1 WHERE artist_id=? AND EXISTS(SELECT 1 FROM artists WHERE id=? AND review_status='approved' AND collection_enabled=1)`, id, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO library_cleanup_skip(artist_id,until_round) VALUES(?,?) ON CONFLICT(artist_id) DO UPDATE SET until_round=MAX(until_round,excluded.until_round)`, id, round+50); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveLibraryArtist pauses collection and removes local artwork metadata.
func (s *Store) RemoveLibraryArtist(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return s.SetCollection(ctx, id, false)
}
