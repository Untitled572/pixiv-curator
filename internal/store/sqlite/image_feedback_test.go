package sqlite

import (
	"context"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestImageFeedbackOverrideNeutralAndPageScopedTags(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 501)
	art := domain.Artwork{ID: 5001, ArtistID: 501, PageCount: 2, MetadataState: "complete", Tags: []string{" Blue Sky ", "blue sky", "Night"}, Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 10, Height: 10}, {Index: 1, OriginalURL: "https://i.pximg.net/b.jpg", Width: 10, Height: 10}}}
	if err := s.UpsertArtwork(ctx, art); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetImageFeedback(ctx, 5001, 8, "like"); err != domain.ErrNotFound {
		t.Fatalf("missing page error=%v", err)
	}
	if _, err := s.SetImageFeedback(ctx, 5001, 0, "like"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetImageFeedback(ctx, 5001, 0, "like"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetImageFeedback(ctx, 5001, 1, "dislike"); err != nil {
		t.Fatal(err)
	}
	counts, err := s.ImageFeedbackTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["blue sky"] != 0 || counts["night"] != 0 {
		t.Fatalf("distinct per-page net counts=%v", counts)
	}
	if _, err = s.SetImageFeedback(ctx, 5001, 1, "neutral"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetImageFeedback(ctx, 5001, 1, "neutral"); err != nil {
		t.Fatal(err)
	}
	counts, err = s.ImageFeedbackTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["blue sky"] != 1 || counts["night"] != 1 {
		t.Fatalf("neutral did not remove page vote: %v", counts)
	}
	if _, err = s.SetImageFeedback(ctx, 5001, 0, "dislike"); err != nil {
		t.Fatal(err)
	}
	counts, err = s.ImageFeedbackTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["blue sky"] != -1 || counts["night"] != -1 {
		t.Fatalf("vote switch accumulated: %v", counts)
	}
	// A complete metadata refresh updates page rows in place and preserves feedback.
	art.Tags = []string{"Blue Sky"}
	if err = s.UpsertArtwork(ctx, art); err != nil {
		t.Fatal(err)
	}
	var value string
	if err = s.db.QueryRowContext(ctx, `SELECT value FROM image_feedback WHERE illust_id=5001 AND page_index=0`).Scan(&value); err != nil || value != "dislike" {
		t.Fatalf("refreshed feedback=%q err=%v", value, err)
	}
}

func TestHistoryFeedbackStableForRepeatedDistributionRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 601)
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 6001, ArtistID: 601, PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/a.jpg", Width: 1, Height: 1}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"h1", "h2"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,'preferred',6001,0,1,?)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.History(ctx, 1, 10)
	if err != nil || len(page.Items) != 2 || page.Items[0].Feedback != "neutral" || page.Items[1].Feedback != "neutral" {
		t.Fatalf("default history feedback=%+v err=%v", page.Items, err)
	}
	if _, err := s.SetImageFeedback(ctx, 6001, 0, "like"); err != nil {
		t.Fatal(err)
	}
	page, err = s.History(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Feedback != "like" || page.Items[1].Feedback != "like" {
		t.Fatalf("history feedback differs by event identity: %+v", page.Items)
	}
}
