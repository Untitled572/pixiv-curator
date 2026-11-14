package sqlite

import (
	"context"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// LibraryCleanupReview returns a random local-only sample of approved artists.
func (s *Store) LibraryCleanupReview(ctx context.Context) ([]domain.LibraryCleanupItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,account,avatar_url,review_status,collection_enabled,remote_followed FROM artists WHERE review_status='approved' AND collection_enabled=1 ORDER BY EXISTS(SELECT 1 FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 WHERE w.artist_id=artists.id AND w.visible=1 AND p.media_state='indexed') DESC, RANDOM() LIMIT 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.LibraryCleanupItem{}
	for rows.Next() {
		var item domain.LibraryCleanupItem
		if err = rows.Scan(&item.Artist.ID, &item.Artist.Name, &item.Artist.Account, &item.Artist.AvatarURL, &item.Artist.ReviewStatus, &item.Artist.CollectionEnabled, &item.Artist.RemoteFollowed); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		rows, err := s.db.QueryContext(ctx, `SELECT CAST(w.id AS TEXT) FROM artworks w JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 WHERE w.artist_id=? AND w.visible=1 AND p.media_state='indexed' ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC LIMIT 3`, items[i].Artist.ID)
		if err != nil {
			return nil, err
		}
		items[i].SampleIllustIDs = []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			items[i].SampleIllustIDs = append(items[i].SampleIllustIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// RemoveLibraryArtist pauses collection and removes local artwork metadata.
func (s *Store) RemoveLibraryArtist(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return s.SetCollection(ctx, id, false)
}
