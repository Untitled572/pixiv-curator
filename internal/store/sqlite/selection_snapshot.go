package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// selectionSnapshot is immutable after publication. Each view groups stable page
// identities by artist and artwork. Live SQL remains authoritative for dynamic
// eligibility and the final transaction.
type snapshotPageKey struct {
	illust int64
	page   int
}
type snapshotWork struct {
	kind, rating, ai string
	created          int64
	bookmarks, views sql.NullInt64
	tags             []string
}
type selectionSnapshot struct {
	revision int64
	works    map[int64]*snapshotWork
	builtAt  time.Time
	views    map[string]map[int64]map[int64][]domain.SelectedPage
}

// StartSelectionSnapshots starts one refresh worker bound to ctx. Calling it
// more than once is harmless. A one-slot channel coalesces bursts of updates.
func (s *Store) StartSelectionSnapshots(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	started := false
	s.selectionSnapshotOnce.Do(func() {
		started = true
		go func() {
			defer close(done)
			reader, err := sql.Open("sqlite", s.selectionSnapshotDSN)
			if err != nil {
				return
			}
			defer reader.Close()
			reader.SetMaxOpenConns(1)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			s.rebuildSelectionSnapshotWith(ctx, reader)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.rebuildSelectionSnapshotWith(ctx, reader)
				case <-s.selectionSnapshotRefresh:
					timer := time.NewTimer(250 * time.Millisecond)
				debounce:
					for {
						select {
						case <-ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
							break debounce
						case <-s.selectionSnapshotRefresh:
							if !timer.Stop() {
								select {
								case <-timer.C:
								default:
								}
							}
							timer.Reset(250 * time.Millisecond)
						}
					}
					s.rebuildSelectionSnapshotWith(ctx, reader)
				}
			}
		}()
	})
	if !started {
		close(done)
	}
	return done
}
func (s *Store) RequestSelectionSnapshotRefresh() {
	select {
	case s.selectionSnapshotRefresh <- struct{}{}:
	default:
	}
}
func (s *Store) rebuildSelectionSnapshot(ctx context.Context) {
	s.rebuildSelectionSnapshotWith(ctx, s.db)
}

func (s *Store) rebuildSelectionSnapshotWith(ctx context.Context, reader *sql.DB) {
	tx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM selection_catalog_revision WHERE id=1`).Scan(&revision); err != nil {
		return
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.artist_id,p.page_index,p.width,p.height,p.original_url,p.preview_url,a.created_at,a.bookmarks,a.views,a.kind,a.rating,a.ai_status FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE ar.review_status='approved' AND ar.collection_enabled=1 AND a.visible=1 AND a.metadata_state='complete' AND a.kind IN ('illust','manga') AND p.media_state='indexed'`)
	if err != nil {
		return
	}
	next := &selectionSnapshot{builtAt: time.Now(), revision: revision, works: map[int64]*snapshotWork{}, views: map[string]map[int64]map[int64][]domain.SelectedPage{"any": {}, "landscape": {}, "portrait": {}}}
	for rows.Next() {
		var p domain.SelectedPage
		var created int64
		var kind, rating, ai string
		var bookmarks, views sql.NullInt64
		if err = rows.Scan(&p.IllustID, &p.ArtistID, &p.PageIndex, &p.Width, &p.Height, &p.OriginalURL, &p.PreviewURL, &created, &bookmarks, &views, &kind, &rating, &ai); err != nil {
			rows.Close()
			return
		}
		if !domain.ValidImageURL(p.OriginalURL) {
			continue
		}
		if next.works[p.IllustID] == nil {
			next.works[p.IllustID] = &snapshotWork{created: created, bookmarks: bookmarks, views: views, kind: kind, rating: rating, ai: ai}
		}
		names := []string{"any"}
		if p.Width > 0 && p.Height > 0 {
			if int64(p.Width)*10 > int64(p.Height)*11 {
				names = append(names, "landscape")
			} else if int64(p.Height)*10 > int64(p.Width)*11 {
				names = append(names, "portrait")
			}
		}
		for _, name := range names {
			view := next.views[name]
			if view[p.ArtistID] == nil {
				view[p.ArtistID] = map[int64][]domain.SelectedPage{}
			}
			view[p.ArtistID][p.IllustID] = append(view[p.ArtistID][p.IllustID], p)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return
	}
	if err = rows.Close(); err != nil {
		return
	}
	rows, err = tx.QueryContext(ctx, `SELECT at.illust_id,t.normalized_name FROM artwork_tags at JOIN tags t ON t.id=at.tag_id`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id int64
		var tag string
		if err = rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return
		}
		if w := next.works[id]; w != nil {
			w.tags = append(w.tags, tag)
		}
	}
	if rows.Err() != nil {
		rows.Close()
		return
	}
	if rows.Close() != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	s.selectionSnapshot.Store(next)
}

// snapshotEligibleWorks always lets SQLite apply the current hard predicates.
// It only substitutes cached metadata and tags after revision equality is read
// in the same transaction, so stale snapshots never narrow the candidate set.
func (s *Store) snapshotEligibleWorks(ctx context.Context, tx *sql.Tx, rules domain.SelectionRules, where string, args []any, artistID int64, accountID string, recentIDs []int64) (map[int64]*stratifiedWork, bool, error) {
	snap := s.selectionSnapshot.Load()
	orientation := normalizedOrientation(rules.Orientation)
	if snap == nil || rules.OrientationFallback != "" || (orientation != "any" && orientation != "landscape" && orientation != "portrait") {
		return nil, false, nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM selection_catalog_revision WHERE id=1`).Scan(&revision); err != nil {
		return nil, false, err
	}
	if revision != snap.revision {
		s.RequestSelectionSnapshotRefresh()
		return nil, false, nil
	}
	if artistID == 0 && accountID == "" && rules.MinWidth == 0 && rules.MinHeight == 0 && len(rules.IncludeTags) == 0 && len(rules.ExcludeTags) == 0 {
		return snapshotCommonWorks(ctx, tx, snap, orientation, rules, recentIDs)
	}
	return nil, false, nil
}

func snapshotCommonWorks(ctx context.Context, tx *sql.Tx, snap *selectionSnapshot, orientation string, rules domain.SelectionRules, recentIDs []int64) (map[int64]*stratifiedWork, bool, error) {
	recent := map[int64]bool{}
	for _, id := range recentIDs {
		recent[id] = true
	}
	disliked := map[snapshotPageKey]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT illust_id,page_index FROM image_feedback WHERE value='dislike'`)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var id int64
		var page int
		if err = rows.Scan(&id, &page); err != nil {
			rows.Close()
			return nil, false, err
		}
		disliked[snapshotPageKey{id, page}] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	excluded := map[int64]bool{}
	rows, err = tx.QueryContext(ctx, `SELECT illust_id FROM artwork_preferences WHERE value='dislike'`)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, false, err
		}
		excluded[id] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	works := map[int64]*stratifiedWork{}
	for artist, artistWorks := range snap.views[orientation] {
		for id, pages := range artistWorks {
			meta := snap.works[id]
			if meta == nil {
				return nil, false, nil
			}
			if recent[id] || excluded[id] || (rules.ExcludeManga && meta.kind == "manga") || !allowedSnapshot(meta.rating, rules.AllowedRatings) || !allowedSnapshot(meta.ai, rules.AllowedAI) {
				continue
			}
			for _, p := range pages {
				if disliked[snapshotPageKey{id, p.PageIndex}] {
					continue
				}
				w := works[id]
				if w == nil {
					w = &stratifiedWork{id: id, artist: artist, created: meta.created, bookmarks: meta.bookmarks, views: meta.views, tags: meta.tags, multiplier: 1}
					works[id] = w
				}
				w.pages = append(w.pages, selectionPage{page: p})
			}
		}
	}
	return works, true, nil
}
func allowedSnapshot(value string, allowed []string) bool {
	if allowed == nil {
		return value == "safe" || value == "r18" || value == "r18g" || value == "non_ai" || value == "ai" || value == "unknown"
	}
	for _, v := range allowed {
		if value == v {
			return true
		}
	}
	return false
}
