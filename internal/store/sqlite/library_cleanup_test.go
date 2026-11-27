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

func TestLibraryCleanupCapsShortWorkReasonPerBatch(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for id := int64(1); id <= 10; id++ {
		seedEligibleArtist(t, s, id, "approved")
		works, pages := int64(1), 1
		if id <= 6 {
			works, pages = 10, 2
		}
		for i := range works {
			seedSelectionArtwork(t, s, id*100+i, id, "safe", "non_ai", nil, pages)
		}
	}
	_, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 6 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	short := 0
	for _, item := range items {
		if item.Artist.ID <= 6 {
			if item.AuditReason != cleanupShortWorkReason {
				t.Fatalf("expected other artists before more short-work cards: %+v", item)
			}
			short++
		}
	}
	if short != 2 {
		t.Fatalf("short-work cards=%d, want 2", short)
	}
}

func TestLibraryCleanupShortSamplesAndRemoveLastPages(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1, "approved")
	seedEligibleArtist(t, s, 2, "approved")
	for id := int64(101); id <= 135; id++ {
		seedSelectionArtwork(t, s, id, 1, "safe", "non_ai", nil, 3)
	}
	seedSelectionArtwork(t, s, 140, 1, "safe", "non_ai", nil, 2)
	seedSelectionArtwork(t, s, 141, 1, "safe", "non_ai", nil, 4)
	seedSelectionArtwork(t, s, 201, 2, "safe", "non_ai", nil, 2)
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET published_at=id;
UPDATE artworks SET retained_page_indices='[0,2]' WHERE artist_id=1 AND page_count=3;
DELETE FROM image_pages WHERE illust_id BETWEEN 101 AND 135 AND page_index=1;
INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id)
VALUES ('first','preferred',101,0,1,'trim-test'),('last','preferred',101,2,1,'trim-test')`); err != nil {
		t.Fatal(err)
	}
	round, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 2 || items[0].Artist.ID != 1 || len(items[0].Samples) < 3 {
		t.Fatalf("round=%d items=%+v err=%v", round, items, err)
	}
	samples := items[0].Samples[:3]
	for _, sample := range samples {
		id, err := strconv.ParseInt(sample.IllustID, 10, 64)
		if err != nil || (id != 140 && (id < 107 || id > 135)) || sample.PageIndex != 0 {
			t.Fatalf("sample outside latest 30 pageable short works: %+v err=%v", sample, err)
		}
		want := []int{0, 2}
		if id == 140 {
			want = []int{0, 1}
		}
		if !reflect.DeepEqual(sample.PageIndices, want) {
			t.Fatalf("sample indices=%v want=%v", sample.PageIndices, want)
		}
	}
	_, refreshed, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(refreshed) == 0 || len(refreshed[0].Samples) < 3 || !reflect.DeepEqual(refreshed[0].Samples[:3], samples) {
		t.Fatalf("same round reshuffled the previews: %+v err=%v", refreshed, err)
	}
	if _, err := s.RemoveLibraryArtistLastPages(ctx, 1, round+1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale round: %v", err)
	}
	if _, err := s.RemoveLibraryArtistLastPages(ctx, 2, round); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unrelated review reason: %v", err)
	}
	if removed, err := s.RemoveLibraryArtistLastPages(ctx, 1, round); err != nil || removed != 36 {
		t.Fatalf("removed=%d err=%v, want 36", removed, err)
	}
	if removed, err := s.RemoveLibraryArtistLastPages(ctx, 1, round); err != nil || removed != 0 {
		t.Fatalf("retry removed=%d err=%v, want 0", removed, err)
	}
	seedSelectionArtwork(t, s, 101, 1, "safe", "non_ai", nil, 3)
	for id, want := range map[int64]int{101: 1, 140: 1, 141: 4, 201: 2} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM image_pages WHERE illust_id=?`, id).Scan(&count); err != nil || count != want {
			t.Fatalf("work %d pages=%d want=%d err=%v", id, count, want, err)
		}
	}
	var retained string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM distribution_history WHERE request_id='trim-test'`).Scan(&retained); err != nil || retained != "first" {
		t.Fatalf("history=%q err=%v, want only the retained first page", retained, err)
	}
}
