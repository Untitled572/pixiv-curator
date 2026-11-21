package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

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
	// Legacy enable timestamps are unknown; qualify artist 1 for the priority pool.
	if _, err := s.db.Exec(`UPDATE artists SET cleanup_enabled_at=NULL; UPDATE artworks SET ai_status='ai',published_at=? WHERE id=101`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	_, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("review items=%d, want 6", len(items))
	}
	foundSample := false
	for _, item := range items {
		if item.Artist.ReviewStatus != "approved" {
			t.Fatalf("included status %q", item.Artist.ReviewStatus)
		}
		if item.Artist.ID == 1 {
			foundSample = len(item.SampleIllustIDs) == 1 && item.SampleIllustIDs[0] == "101"
		}
		if len(item.SampleIllustIDs) > 6 {
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
		{ID: 301, ArtistID: 30, Rating: "r18", PageCount: 2, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 1600, Height: 900}, {Index: 1, OriginalURL: "https://i.pximg.net/b.jpg", Width: 900, Height: 1600}}},
		{ID: 302, ArtistID: 31, Rating: "unknown", PageCount: 2, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/c.jpg", Width: 1100, Height: 1000}, {Index: 1, OriginalURL: "https://i.pximg.net/d.jpg"}}},
	} {
		if err := s.UpsertArtwork(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.CatalogStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ImagePages != 4 || stats.R18ImagePages != 2 || stats.R18ImageRatio != 0.5 ||
		stats.LandscapePages != 1 || stats.PortraitPages != 1 || stats.SquarePages != 1 || stats.UnknownOrientationPages != 1 {
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

func TestCleanupBatchPersistsAndUsesAdaptiveCooldown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cleanup.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 7; id++ {
		seedArtist(t, s, id)
		if _, err = s.DecideArtist(ctx, id, "approve"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`UPDATE artists SET cleanup_enabled_at=NULL`); err != nil {
		t.Fatal(err)
	}
	round, first, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(first) != 6 {
		t.Fatalf("initial: %v %v", first, err)
	}
	id := first[0].Artist.ID
	if err = s.KeepLibraryCleanupArtist(ctx, id, round); err != nil {
		t.Fatal(err)
	}
	_, again, kept, err := s.LibraryCleanupReview(ctx)
	if err != nil || !reflect.DeepEqual(first, again) || len(kept) != 1 || kept[0] != strconv.FormatInt(id, 10) {
		t.Fatalf("reload: %v %v", kept, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, again, kept, err = s.LibraryCleanupReview(ctx)
	if err != nil || !reflect.DeepEqual(first, again) || len(kept) != 1 {
		t.Fatalf("reopen: %v %v", kept, err)
	}
	if _, _, _, err = s.NextLibraryCleanup(ctx, round); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.NextLibraryCleanup(ctx, round); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale next: %v", err)
	}
	var until, minAt, maxAt int64
	if err = s.db.QueryRow(`SELECT until_round,min_at,max_at FROM library_cleanup_skip WHERE artist_id=?`, id).Scan(&until, &minAt, &maxAt); err != nil {
		t.Fatal(err)
	}
	if until != round+30 || maxAt-minAt != int64((120*24*time.Hour)/time.Millisecond) {
		t.Fatalf("cooldown: %d %d %d", until, minAt, maxAt)
	}
}
