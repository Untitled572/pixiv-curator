package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestTrimLibraryArtistPagesKeepsNewestFirstPagesAndFencesResync(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 1)
	if _, err := s.DecideArtist(ctx, 1, "approve"); err != nil {
		t.Fatal(err)
	}
	catalog := make([]domain.Artwork, 0, 10)
	for i := int64(1); i <= 10; i++ {
		published := i
		a := domain.Artwork{ID: 100 + i, ArtistID: 1, PageCount: 3, MetadataState: "complete", PublishedAt: &published, Tags: []string{"tag"}}
		for page := range a.PageCount {
			a.Pages = append(a.Pages, domain.Page{Index: page, OriginalURL: fmt.Sprintf("https://i.pximg.net/%d_p%d.jpg", a.ID, page)})
		}
		catalog = append(catalog, a)
		if err := s.UpsertArtwork(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE artists SET cleanup_enabled_at=NULL;
INSERT INTO image_feedback VALUES (101,0,'like',1);
INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES ('trim','preferred',101,0,1,'trim')`); err != nil {
		t.Fatal(err)
	}
	round, batch, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(batch) != 1 {
		t.Fatalf("round=%d batch size=%d err=%v", round, len(batch), err)
	}
	if _, _, err = s.TrimLibraryArtistPages(ctx, 1, round+1); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale round: %v", err)
	}
	before, after, err := s.TrimLibraryArtistPages(ctx, 1, round)
	if before != 30 || after != 15 || err != nil {
		t.Fatalf("before=%d after=%d err=%v", before, after, err)
	}
	if _, _, err = s.TrimLibraryArtistPages(ctx, 1, round); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("repeated trim: %v", err)
	}
	const storedPages = `SELECT group_concat(illust_id || '/' || page_index) FROM (SELECT illust_id,page_index FROM image_pages ORDER BY illust_id,page_index)`
	var sampled, rescanned string
	if err = s.db.QueryRow(storedPages).Scan(&sampled); err != nil {
		t.Fatal(err)
	}
	for _, a := range catalog {
		if err = s.UpsertArtwork(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.db.QueryRow(storedPages).Scan(&rescanned); err != nil || sampled != rescanned {
		t.Fatalf("retained pages changed on rescan: before %q, after %q, err=%v", sampled, rescanned, err)
	}
	_, batch, _, err = s.LibraryCleanupReview(ctx)
	if err != nil || len(batch) != 1 || !batch[0].Trimmed || batch[0].Summary.Pages != 15 || batch[0].Summary.Artworks != 10 || batch[0].Summary.Likes != 1 || !batch[0].Artist.CollectionEnabled {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	rows, err := s.db.Query(`SELECT illust_id FROM image_pages WHERE page_index=0 ORDER BY illust_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	first := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		first = append(first, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []int64{102, 103, 104, 105, 106, 107, 108, 109, 110}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first-page works=%v want %v", first, want)
	}
	var newlyCollected, history int
	newWork := catalog[len(catalog)-1]
	newWork.ID++
	if err = s.UpsertArtwork(ctx, newWork); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM image_pages WHERE illust_id=?`, newWork.ID).Scan(&newlyCollected); err != nil || newlyCollected != newWork.PageCount {
		t.Fatalf("new work pages=%d err=%v", newlyCollected, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM distribution_history WHERE id='trim'`).Scan(&history); err != nil || history != 0 {
		t.Fatalf("pruned page history rows=%d err=%v", history, err)
	}
}
