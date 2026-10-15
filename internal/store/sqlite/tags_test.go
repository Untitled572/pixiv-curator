package sqlite

import (
	"context"
	"reflect"
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
