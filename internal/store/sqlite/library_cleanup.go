package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

func cleanupItems(ctx context.Context, tx *sql.Tx) ([]domain.LibraryCleanupItem, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed,b.kept,b.slot_type,b.audit_reason,b.trigger_ai_id FROM library_cleanup_batch b JOIN artists a ON a.id=b.artist_id WHERE a.review_status='approved' AND a.collection_enabled=1 ORDER BY b.position`)
	if err != nil {
		return nil, nil, err
	}
	items := []domain.LibraryCleanupItem{}
	kept := []string{}
	triggers := map[int64]sql.NullInt64{}
	for rows.Next() {
		var item domain.LibraryCleanupItem
		var isKept bool
		var trigger sql.NullInt64
		if err = rows.Scan(&item.Artist.ID, &item.Artist.Name, &item.Artist.Account, &item.Artist.AvatarURL, &item.Artist.ReviewStatus, &item.Artist.CollectionEnabled, &item.Artist.RemoteFollowed, &isKept, &item.SlotType, &item.AuditReason, &trigger); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if isKept {
			kept = append(kept, fmt.Sprint(item.Artist.ID))
		}
		triggers[item.Artist.ID] = trigger
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return items, kept, nil
	}
	rules, err := defaultSelectionRulesInTx(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	for i := range items {
		item := &items[i]
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(rating IN ('r18','r18g')),0),COALESCE(SUM(rating<>'unknown'),0),COALESCE(SUM(ai_status='ai'),0),COALESCE(SUM(ai_status<>'unknown'),0),COALESCE(MAX(published_at),0) FROM artworks WHERE artist_id=? AND visible=1`, item.Artist.ID).Scan(&item.Summary.Artworks, &item.Summary.R18, &item.Summary.KnownRating, &item.Summary.AI, &item.Summary.KnownAI, &item.Summary.LatestPublished)
		if err != nil {
			return nil, nil, err
		}
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_pages p JOIN artworks w ON w.id=p.illust_id WHERE w.artist_id=? AND w.visible=1 AND p.media_state='indexed'`, item.Artist.ID).Scan(&item.Summary.Pages)
		if err != nil {
			return nil, nil, err
		}
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(f.value='like'),0),COALESCE(SUM(f.value='dislike'),0) FROM image_feedback f JOIN artworks w ON w.id=f.illust_id WHERE w.artist_id=?`, item.Artist.ID).Scan(&item.Summary.Likes, &item.Summary.Dislikes)
		if err != nil {
			return nil, nil, err
		}
		where, args, e := selectionWhere(rules, item.Artist.ID, "", nil)
		if e != nil {
			return nil, nil, e
		}
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists ar JOIN artworks a INDEXED BY artworks_artist ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where, args...).Scan(&item.Summary.EligiblePages)
		if err != nil {
			return nil, nil, err
		}
		item.SampleIllustIDs = []string{}
		item.Samples = []domain.LibraryCleanupSample{}
		seen := map[string]bool{}
		add := func(id string, page int, reason string) {
			if len(item.Samples) >= 6 || seen[id] {
				return
			}
			seen[id] = true
			item.Samples = append(item.Samples, domain.LibraryCleanupSample{IllustID: id, PageIndex: page, Reason: reason})
			item.SampleIllustIDs = append(item.SampleIllustIDs, id)
		}
		if trigger := triggers[item.Artist.ID]; trigger.Valid {
			var id string
			err = tx.QueryRowContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 AND p.media_state='indexed' WHERE w.id=? AND w.visible=1`, trigger.Int64).Scan(&id)
			if err == nil {
				add(id, 0, "ai")
			} else if err != sql.ErrNoRows {
				return nil, nil, err
			}
		}
		// Prefer the exact disliked page; one sample per work.
		rows, err = tx.QueryContext(ctx, `SELECT CAST(w.id AS TEXT), f.page_index FROM artworks w JOIN image_feedback f ON f.illust_id=w.id AND f.value='dislike' JOIN image_pages p ON p.illust_id=f.illust_id AND p.page_index=f.page_index AND p.media_state='indexed' WHERE w.artist_id=? AND w.visible=1 ORDER BY f.updated_at DESC,w.id DESC`, item.Artist.ID)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			var page int
			if err = rows.Scan(&id, &page); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if len(item.Samples) < 2 {
				add(id, page, "dislike")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(item.Samples) < 2 && (len(item.Samples) == 0 || item.Samples[0].Reason != "ai") {
			rows, err = tx.QueryContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 AND p.media_state='indexed' WHERE w.artist_id=? AND w.visible=1 AND w.ai_status='ai' AND w.published_at>=? ORDER BY w.published_at DESC,w.id DESC`, item.Artist.ID, time.Now().Add(-30*24*time.Hour).UnixMilli())
			if err != nil {
				return nil, nil, err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return nil, nil, err
				}
				if !seen[id] {
					add(id, 0, "ai")
					break
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, nil, err
			}
		}
		risk := len(item.Samples)
		recentLimit := 3
		if risk == 2 {
			recentLimit = 2
		}
		rows, err = tx.QueryContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 AND p.media_state='indexed' WHERE w.artist_id=? AND w.visible=1 ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC,CASE WHEN w.kind='illust' THEN 0 ELSE 1 END LIMIT 6`, item.Artist.ID)
		if err != nil {
			return nil, nil, err
		}
		recent := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			recent = append(recent, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
		for _, id := range recent {
			if len(item.Samples) >= risk+recentLimit {
				break
			}
			add(id, 0, "")
		}
		historicalArgs := append([]any{item.Artist.ID}, args...)
		rows, err = tx.QueryContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages preview ON preview.illust_id=w.id AND preview.page_index=0 AND preview.media_state='indexed' WHERE w.artist_id=? AND w.visible=1 ORDER BY CASE WHEN w.kind='illust' AND EXISTS (SELECT 1 FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where+` AND a.id=w.id) THEN 0 ELSE 1 END,COALESCE(w.bookmarks,0) DESC,w.id DESC LIMIT 6`, historicalArgs...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			add(id, 0, "")
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
	if err = coolCleanupBatch(ctx, tx, round); err != nil {
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
	if err = startCleanupCooldown(ctx, tx, id, round, true); err != nil {
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
