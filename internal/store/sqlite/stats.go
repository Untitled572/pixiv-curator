package sqlite

import (
	"context"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// CatalogStats reads aggregate counts from the local database only.
func (s *Store) CatalogStats(ctx context.Context) (domain.CatalogStats, error) {
	var stats domain.CatalogStats
	var knownSizes int64
	err := s.db.QueryRowContext(ctx, `WITH pages AS (
	  SELECT COUNT(*) AS total,
	     COUNT(*) FILTER (WHERE a.rating IN ('r18','r18g')) AS adult,
	     COUNT(*) FILTER (WHERE p.media_state='indexed') AS indexed,
	     COUNT(*) FILTER (WHERE p.media_state='unavailable') AS unavailable,
	     COUNT(*) FILTER (WHERE p.width>0 AND p.height>0) AS known_size,
	     COUNT(*) FILTER (WHERE p.width>0 AND p.height>0 AND p.width*10>p.height*11) AS landscape,
	     COUNT(*) FILTER (WHERE p.width>0 AND p.height>0 AND p.height*10>p.width*11) AS portrait
	  FROM image_pages p JOIN artworks a ON a.id=p.illust_id
	 ) SELECT
	 (SELECT COUNT(*) FROM accounts WHERE deleted=0),
	 (SELECT COUNT(*) FROM artists WHERE review_status IN ('approved','blocked')),
	 (SELECT COUNT(*) FROM artists WHERE review_status IN ('approved','blocked')),
	 (SELECT COUNT(*) FROM artists WHERE review_status='blocked'),
	 (SELECT COUNT(*) FROM artists WHERE review_status='approved' AND collection_enabled=1),
	 (SELECT COUNT(*) FROM artworks),
	 total, adult, COALESCE(1.0*adult/NULLIF(total,0),0), indexed, unavailable,
	 known_size, landscape, portrait,
	 (SELECT COUNT(*) FROM artworks WHERE rating='unknown'),
	 (SELECT COUNT(*) FROM artworks WHERE ai_status='unknown') FROM pages`).Scan(
		&stats.Accounts, &stats.Artists, &stats.LibraryArtists, &stats.BlockedArtists,
		&stats.CollectingArtists, &stats.Artworks, &stats.ImagePages, &stats.R18ImagePages,
		&stats.R18ImageRatio, &stats.IndexedPages,
		&stats.UnavailablePages, &knownSizes, &stats.LandscapePages, &stats.PortraitPages,
		&stats.UnknownRatingArtworks, &stats.UnknownAIArtworks,
	)
	stats.SquarePages = knownSizes - stats.LandscapePages - stats.PortraitPages
	stats.UnknownOrientationPages = stats.ImagePages - knownSizes
	return stats, err
}
