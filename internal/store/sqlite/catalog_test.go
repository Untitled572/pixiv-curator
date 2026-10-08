package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestUpsertArtistPreservesLocalReviewAndCollectionState(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 301, Name: "first", ReviewStatus: "pending"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=301`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 301, Name: "updated", Account: "account"}, "following", "run-1"); err != nil {
		t.Fatal(err)
	}

	artists, err := s.Artists(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 1 {
		t.Fatalf("artist count = %d, want 1", len(artists))
	}
	a := artists[0]
	if a.Name != "updated" || a.Account != "account" {
		t.Fatalf("upstream fields were not updated: %+v", a)
	}
	if a.ReviewStatus != "approved" || !a.CollectionEnabled {
		t.Fatalf("local review state changed: %+v", a)
	}
}

func TestUpsertArtworkPreservesVisibilityAndUnknownEnums(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 401)
	artwork := domain.Artwork{ID: 402, ArtistID: 401, Title: "initial", Kind: "illust", Rating: "safe", AIStatus: "non_ai", MetadataState: "complete", PageCount: 1, Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 100, Height: 100}}}
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET visible=0 WHERE id=402`); err != nil {
		t.Fatal(err)
	}
	artwork.Title = "updated"
	artwork.Kind = "future-kind"
	artwork.Rating = "future-rating"
	artwork.AIStatus = "future-ai-value"
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}

	var visible int
	var kind, rating, aiStatus, title string
	if err := s.db.QueryRowContext(ctx, `SELECT visible,kind,rating,ai_status,title FROM artworks WHERE id=402`).Scan(&visible, &kind, &rating, &aiStatus, &title); err != nil {
		t.Fatal(err)
	}
	if visible != 0 || title != "updated" {
		t.Fatalf("visible/title = %d/%q, want 0/updated", visible, title)
	}
	if kind != "unknown" || rating != "unknown" || aiStatus != "unknown" {
		t.Fatalf("unknown enums were not preserved as unknown: kind=%q rating=%q ai=%q", kind, rating, aiStatus)
	}
}

func TestCompleteArtworkPageShrinkMarksRemovedPagesUnavailable(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 501)
	artwork := domain.Artwork{ID: 502, ArtistID: 501, PageCount: 2, MetadataState: "complete", Pages: []domain.Page{
		{Index: 0, OriginalURL: "https://i.pximg.net/0.jpg", Width: 100, Height: 100},
		{Index: 1, OriginalURL: "https://i.pximg.net/1.jpg", Width: 100, Height: 100},
	}}
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}
	artwork.PageCount = 1
	artwork.Pages = artwork.Pages[:1]
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}

	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT media_state FROM image_pages WHERE illust_id=502 AND page_index=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unavailable" {
		t.Fatalf("removed page state = %q, want unavailable", state)
	}
	var pageCount int
	if err := s.db.QueryRowContext(ctx, `SELECT page_count FROM artworks WHERE id=502`).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if pageCount != 1 {
		t.Fatalf("page_count = %d, want 1", pageCount)
	}
}

func TestUpsertArtworkRejectsUntrustedImageURLs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 551)
	cases := []struct {
		name     string
		original string
		preview  string
	}{
		{name: "http original", original: "http://i.pximg.net/a.jpg"},
		{name: "other original host", original: "https://example.com/a.jpg"},
		{name: "other preview host", original: "https://i.pximg.net/a.jpg", preview: "https://example.com/a.jpg"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := domain.Artwork{ID: int64(552 + i), ArtistID: 551, PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: tc.original, PreviewURL: tc.preview, Width: 1, Height: 1}}}
			if err := s.UpsertArtwork(ctx, a); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("UpsertArtwork error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestPartialArtworkDoesNotDeleteTags(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 601)
	artwork := domain.Artwork{ID: 602, ArtistID: 601, PageCount: 1, MetadataState: "complete", Tags: []string{"blue hair", "night"}, Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 1, Height: 1}}}
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}
	artwork.MetadataState = "pending"
	artwork.Tags = []string{"partial snapshot only"}
	if err := s.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT t.normalized_name FROM tags t JOIN artwork_tags at ON at.tag_id=t.id WHERE at.illust_id=602 ORDER BY t.normalized_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			t.Fatal(err)
		}
		got = append(got, tag)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "blue hair" || got[1] != "night" {
		t.Fatalf("tags = %v, want [blue hair night]", got)
	}
}

func TestSettingCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	first := json.RawMessage(`{"enabled":true}`)
	second := json.RawMessage(`{"enabled":false}`)
	if err := s.PutSetting(ctx, "preferences", first, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "preferences", second, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale insert error = %v, want ErrConflict", err)
	}
	value, version, err := s.Setting(ctx, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != string(first) || version != 1 {
		t.Fatalf("setting = %s at version %d, want original value at version 1", value, version)
	}
	if err := s.PutSetting(ctx, "preferences", second, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "preferences", first, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}
	value, version, err = s.Setting(ctx, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != string(second) || version != 2 {
		t.Fatalf("setting = %s at version %d, want updated value at version 2", value, version)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedArtist(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.UpsertArtist(context.Background(), domain.Artist{ID: id, Name: "artist"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
}
