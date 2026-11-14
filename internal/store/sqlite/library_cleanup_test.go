package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestLibraryCleanupReviewBoundAndRemove(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for id := int64(1); id <= 7; id++ {
		seedArtist(t, s, id)
		if _, err := s.DecideArtist(ctx, id, "approve"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 101, ArtistID: 1, Rating: "r18g", PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 10, Height: 10}}}); err != nil {
		t.Fatal(err)
	}
	items, err := s.LibraryCleanupReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("review items=%d, want 5", len(items))
	}
	foundSample := false
	for _, item := range items {
		if item.Artist.ReviewStatus != "approved" {
			t.Fatalf("included status %q", item.Artist.ReviewStatus)
		}
		if item.Artist.ID == 1 {
			foundSample = len(item.SampleIllustIDs) == 1 && item.SampleIllustIDs[0] == "101"
		}
		if len(item.SampleIllustIDs) > 3 {
			t.Fatalf("sample count=%d", len(item.SampleIllustIDs))
		}
	}
	if !foundSample {
		t.Fatal("artist with local sample was not prioritized")
	}
	if _, err = s.Enqueue(ctx, "hydrate_artwork", "cleanup-hydrate", json.RawMessage(`{"illust_id":101}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enqueue(ctx, "repair_media", "cleanup-repair", json.RawMessage(`{"illust_id":101}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enqueue(ctx, "follow_artist", "cleanup-follow", json.RawMessage(`{"artist_id":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveLibraryArtist(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.RemoveLibraryArtist(ctx, 1); err != nil {
		t.Fatalf("repeat remove: %v", err)
	}
	var state string
	var enabled bool
	if err = s.db.QueryRow(`SELECT review_status,collection_enabled FROM artists WHERE id=1`).Scan(&state, &enabled); err != nil {
		t.Fatal(err)
	}
	if state != "approved" || enabled {
		t.Fatalf("state=%q enabled=%v", state, enabled)
	}
	var active, sources int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE dedupe_key IN ('cleanup-hydrate','cleanup-repair') AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM artist_sources WHERE artist_id=1`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if active != 0 || sources == 0 {
		t.Fatalf("active jobs=%d sources=%d", active, sources)
	}
}

func TestCatalogStatsR18ImagePageRatioIncludesUnknownDenominator(t *testing.T) {
	s := openTestStore(t)
	for _, id := range []int64{30, 31} {
		seedArtist(t, s, id)
		if _, err := s.DecideArtist(context.Background(), id, "approve"); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []domain.Artwork{
		{ID: 301, ArtistID: 30, Rating: "r18", PageCount: 2, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 1, Height: 1}, {Index: 1, OriginalURL: "https://i.pximg.net/b.jpg", Width: 1, Height: 1}}},
		{ID: 302, ArtistID: 31, Rating: "unknown", PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/c.jpg", Width: 1, Height: 1}}},
	} {
		if err := s.UpsertArtwork(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.CatalogStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ImagePages != 3 || stats.R18ImagePages != 2 || stats.R18ImageRatio < 0.6666 || stats.R18ImageRatio > 0.6667 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestPausePurgesCatalogAndPreservesArtist(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 991)
	if _, err := s.DecideArtist(ctx, 991, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 992, ArtistID: 991, PageCount: 1, Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", PreviewURL: "https://i.pximg.net/preview.jpg", Width: 10, Height: 10}}}); err != nil {
		t.Fatal(err)
	}
	var purged []string
	s.SetPreviewPurgeHook(func(urls []string) error { purged = urls; return nil })
	if err := s.SetCollection(ctx, 991, false); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 1 || purged[0] != "https://i.pximg.net/preview.jpg" {
		t.Fatalf("purged previews: %v", purged)
	}
	var state string
	var enabled bool
	var artworks, sources int
	if err := s.db.QueryRow(`SELECT review_status,collection_enabled,(SELECT COUNT(*) FROM artworks WHERE artist_id=991),(SELECT COUNT(*) FROM artist_sources WHERE artist_id=991) FROM artists WHERE id=991`).Scan(&state, &enabled, &artworks, &sources); err != nil {
		t.Fatal(err)
	}
	if state != "approved" || enabled || artworks != 0 || sources == 0 {
		t.Fatalf("state=%s enabled=%v artworks=%d sources=%d", state, enabled, artworks, sources)
	}
	urls, err := s.LivePreviewURLs(ctx)
	if err != nil || len(urls) != 0 {
		t.Fatalf("live previews=%v err=%v", urls, err)
	}
	if err = s.SetCollection(ctx, 991, true); err != nil {
		t.Fatal(err)
	}
	var lastSynced bool
	if err = s.db.QueryRow(`SELECT last_synced_at IS NULL FROM artists WHERE id=991`).Scan(&lastSynced); err != nil || !lastSynced {
		t.Fatalf("last synced reset=%v err=%v", lastSynced, err)
	}
}
