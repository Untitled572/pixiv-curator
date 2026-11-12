package sqlite

import (
	"context"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// CatalogStats reads aggregate counts from the local database only.
func (s *Store) CatalogStats(ctx context.Context) (domain.CatalogStats, error) {
	var stats domain.CatalogStats
	err := s.db.QueryRowContext(ctx, `SELECT
	 (SELECT COUNT(*) FROM accounts WHERE deleted=0),
	 (SELECT COUNT(*) FROM artists),
	 (SELECT COUNT(*) FROM artists WHERE review_status IN ('approved','blocked')),
	 (SELECT COUNT(*) FROM artists WHERE review_status='blocked'),
	 (SELECT COUNT(*) FROM artists WHERE review_status='approved' AND collection_enabled=1),
	 (SELECT COUNT(*) FROM artworks),
	 (SELECT COUNT(*) FROM image_pages),
	 (SELECT COUNT(*) FROM image_pages WHERE media_state='indexed'),
	 (SELECT COUNT(*) FROM image_pages WHERE media_state='unavailable'),
	 (SELECT COUNT(*) FROM artworks WHERE rating='unknown'),
	 (SELECT COUNT(*) FROM artworks WHERE ai_status='unknown')`).Scan(
		&stats.Accounts, &stats.Artists, &stats.LibraryArtists, &stats.BlockedArtists,
		&stats.CollectingArtists, &stats.Artworks, &stats.ImagePages, &stats.IndexedPages,
		&stats.UnavailablePages, &stats.UnknownRatingArtworks, &stats.UnknownAIArtworks,
	)
	return stats, err
}
