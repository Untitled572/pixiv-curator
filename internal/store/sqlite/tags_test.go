package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestArtistTagsAutomaticOverrideAndRestore(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7101)
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=7101`); err != nil {
		t.Fatal(err)
	}
	for i, tags := range [][]string{{"cat", "blue"}, {"cat", "night"}, {"cat"}, {"blue"}} {
		if err := s.UpsertArtwork(ctx, domain.Artwork{ID: int64(7110 + i), ArtistID: 7101, MetadataState: "complete", Tags: tags}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ArtistTagPage(ctx, "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0].Tags, []string{"cat", "blue", "night"}) {
		t.Fatalf("automatic tags=%v", items)
	}
	if err = s.SetArtistTagOverride(ctx, 7101, []string{"Human", " human ", "Curated"}); err != nil {
		t.Fatal(err)
	}
	items, err = s.ArtistTagPage(ctx, "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Human", "Curated"}; !reflect.DeepEqual(items[0].Tags, want) {
		t.Fatalf("manual tags=%v want %v", items[0].Tags, want)
	}
	items, err = s.ArtistTagPage(ctx, "cat", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("manual override should replace auto tags: %v", items)
	}
	if err = s.SetArtistTagOverride(ctx, 7101, []string{}); err != nil {
		t.Fatal(err)
	}
	items, err = s.ArtistTagPage(ctx, "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Tags) != 0 {
		t.Fatalf("explicit empty override=%v", items)
	}
	if err = s.ClearArtistTagOverride(ctx, 7101); err != nil {
		t.Fatal(err)
	}
	items, err = s.ArtistTagPage(ctx, "", "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items[0].Tags, []string{"cat", "blue", "night"}) {
		t.Fatalf("restored tags=%v", items[0].Tags)
	}
}

func TestArtistTagAccountAndCursorIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []int64{7201, 7202, 7203} {
		seedArtist(t, s, id)
		if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetArtistTagOverride(ctx, id, []string{"same"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts(id,name,token_file,created_at,updated_at) VALUES('one','One','/secret',1,1),('two','Two','/secret',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]any{{"one", int64(7201)}, {"one", int64(7203)}, {"two", int64(7202)}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES(?,?,1)`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ArtistTagPage(ctx, "same", "one", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != 7201 || page[1].ID != 7203 {
		t.Fatalf("first page should include limit+1 for stable cursor: %+v", page)
	}
	page, err = s.ArtistTagPage(ctx, "same", "one", 7201, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != 7203 {
		t.Fatalf("account/cursor page=%+v", page)
	}
	page, err = s.ArtistTagPage(ctx, "same", "two", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != 7202 {
		t.Fatalf("account isolation=%+v", page)
	}
}

func TestArtistTagStatsPaginationAndDeletedAccountIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for id := int64(7301); id < 7331; id++ {
		seedArtist(t, s, id)
		if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetArtistTagOverride(ctx, id, []string{"match"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts(id,name,token_file,created_at,updated_at) VALUES('gone','Gone','private',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id) VALUES('gone',7301)`); err != nil {
		t.Fatal(err)
	}
	items, total, libraryTotal, pages, next, err := s.ArtistTagPageStats(ctx, "match", "", 2, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 12 || items[0].ID != 7313 || total != 30 || libraryTotal != 30 || pages != 3 || next != "7324" {
		t.Fatalf("page=%+v total=%d library=%d pages=%d next=%q", items, total, libraryTotal, pages, next)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET deleted=1 WHERE id='gone'`); err != nil {
		t.Fatal(err)
	}
	items, total, _, _, _, err = s.ArtistTagPageStats(ctx, "match", "gone", 1, 24)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("deleted account leaked catalog: items=%v total=%d err=%v", items, total, err)
	}
	items, total, _, pages, next, err = s.ArtistTagPageStats(ctx, "absent", "", 1, 24)
	if err != nil || total != 0 || len(items) != 0 || pages != 0 || next != "" {
		t.Fatalf("empty page items=%v total=%d pages=%d next=%q err=%v", items, total, pages, next, err)
	}
}

func TestArtistTagFiltersPreserveUnicodeCaseFolding(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []int64{7351, 7352} {
		seedArtist(t, s, id)
		if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetArtistTagOverride(ctx, 7351, []string{"Καλημέρα"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 7360, ArtistID: 7352, MetadataState: "complete", Tags: []string{"ΜΟΥΣΙΚΉ"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filter string
		artist int64
	}{{"καλημέρα", 7351}, {"μουσική", 7352}} {
		items, total, _, pages, _, err := s.ArtistTagPageStats(ctx, tc.filter, "", 1, 10)
		if err != nil || total != 1 || pages != 1 || len(items) != 1 || items[0].ID != tc.artist {
			t.Fatalf("unicode filter %q items=%+v total=%d pages=%d err=%v", tc.filter, items, total, pages, err)
		}
		legacy, err := s.ArtistTagPage(ctx, strings.ToUpper(tc.filter), "", 0, 10)
		if err != nil || len(legacy) != 1 || legacy[0].ID != tc.artist {
			t.Fatalf("legacy unicode filter %q items=%+v err=%v", tc.filter, legacy, err)
		}
	}
}

func TestCatalogStatsCountUnknownAndBlocked(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7401)
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='blocked',collection_enabled=1 WHERE id=7401`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 7410, ArtistID: 7401, MetadataState: "complete", PageCount: 2, Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", State: "indexed"}, {Index: 1, State: "unavailable"}}}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.CatalogStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Artists != 1 || stats.LibraryArtists != 1 || stats.BlockedArtists != 1 || stats.CollectingArtists != 0 || stats.Artworks != 1 || stats.ImagePages != 2 || stats.IndexedPages != 1 || stats.UnavailablePages != 1 || stats.UnknownRatingArtworks != 1 || stats.UnknownAIArtworks != 1 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestCatalogStatsEmptyCatalog(t *testing.T) {
	s := openTestStore(t)
	stats, err := s.CatalogStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats != (domain.CatalogStats{}) {
		t.Fatalf("empty catalog stats=%+v", stats)
	}
}
