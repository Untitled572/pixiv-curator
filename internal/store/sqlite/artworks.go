package sqlite

import (
	"context"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func (s *Store) Artworks(ctx context.Context, after int64, limit int, artistID int64) ([]domain.Artwork, error) {
	if after < 0 || artistID < 0 || limit < 1 || limit > 200 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,artist_id,title,caption,kind,page_count,rating,ai_status,bookmarks,views,published_at,metadata_state,visible FROM artworks WHERE id>? AND (?=0 OR artist_id=?) ORDER BY id LIMIT ?`, after, artistID, artistID, limit)
	if err != nil {
		return nil, err
	}
	result := []domain.Artwork{}
	for rows.Next() {
		var a domain.Artwork
		if err = rows.Scan(&a.ID, &a.ArtistID, &a.Title, &a.Caption, &a.Kind, &a.PageCount, &a.Rating, &a.AIStatus, &a.Bookmarks, &a.Views, &a.PublishedAt, &a.MetadataState, &a.Visible); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		a := &result[i]
		tags, err := s.db.QueryContext(ctx, `SELECT t.display_name FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id=? ORDER BY t.id LIMIT 1000`, a.ID)
		if err != nil {
			return nil, err
		}
		for tags.Next() {
			var tag string
			if err = tags.Scan(&tag); err != nil {
				tags.Close()
				return nil, err
			}
			a.Tags = append(a.Tags, tag)
		}
		err = tags.Err()
		tags.Close()
		if err != nil {
			return nil, err
		}
		pages, err := s.db.QueryContext(ctx, `SELECT page_index,original_url,preview_url,width,height,media_state FROM image_pages WHERE illust_id=? ORDER BY page_index LIMIT 1000`, a.ID)
		if err != nil {
			return nil, err
		}
		for pages.Next() {
			var p domain.Page
			if err = pages.Scan(&p.Index, &p.OriginalURL, &p.PreviewURL, &p.Width, &p.Height, &p.State); err != nil {
				pages.Close()
				return nil, err
			}
			a.Pages = append(a.Pages, p)
		}
		err = pages.Err()
		pages.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
