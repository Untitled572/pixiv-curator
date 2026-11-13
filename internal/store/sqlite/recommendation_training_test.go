package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
)

func TestRecommendationRegressionExamplesUseCurrentExplicitStatus(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	insertTrainingRun(t, s, "train", `{"recent_approved_artist_ids":[501,502]}`)
	insertTrainingRun(t, s, "old-run", `{}`)

	for _, item := range []struct {
		id     int64
		status string
		tags   []string
	}{
		{501, "approved", []string{" Blue ", "blue", "Cat"}},
		{502, "rejected", []string{"No thanks"}},
		{503, "pending", []string{"pending-tag"}},
		{504, "blocked", []string{"blocked-tag"}},
		{505, "approved", nil},
		{506, "approved", nil},
		{507, "approved", []string{""}},
	} {
		seedTrainingArtist(t, s, item.id, item.status)
		if len(item.tags) > 0 {
			seedTrainingArtwork(t, s, item.id, item.id*10, item.tags, item.id)
		}
	}
	insertTrainingSources(t, s, "old-run", 501, []string{"related_artist", "related_artist", "independent_exploration"})
	insertTrainingSources(t, s, "old-run", 502, []string{"tag_search"})
	insertTrainingSources(t, s, "old-run", 503, []string{"recommendation_feed"})
	insertTrainingSources(t, s, "old-run", 504, []string{"related_artwork"})
	insertTrainingSources(t, s, "old-run", 505, []string{"related_artwork", "independent_exploration"})
	insertTrainingSources(t, s, "old-run", 506, []string{"independent_exploration"})

	got, err := s.RecommendationRegressionExamples(ctx, "train")
	if err != nil {
		t.Fatal(err)
	}
	want := []recommend.RegressionExample{
		{Tags: []string{"blue", "cat"}, Sources: []string{"related_artist"}, Liked: true, Weight: 0.2},
		{Tags: []string{"no thanks"}, Sources: []string{"tag_search"}, Liked: false, Weight: 1},
		{Sources: []string{"related_artwork"}, Liked: true, Weight: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("training examples = %#v, want %#v", got, want)
	}
}

func TestRecommendationRegressionExamplesUseRecent100AndTop32Tags(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	insertTrainingRun(t, s, "train", `{}`)
	seedTrainingArtist(t, s, 800, "approved")
	for i := int64(1); i <= 100; i++ {
		tags := []string{"popular"}
		if i == 1 {
			for tag := 0; tag < 33; tag++ {
				tags = append(tags, fmt.Sprintf("tag-%02d", tag))
			}
		}
		seedTrainingArtwork(t, s, 800, 8000+i, tags, 1000+i)
	}
	seedTrainingArtwork(t, s, 800, 8999, []string{"only-old"}, 1)

	examples, err := s.RecommendationRegressionExamples(ctx, "train")
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 1 {
		t.Fatalf("example count=%d, want one", len(examples))
	}
	if len(examples[0].Tags) != 32 || examples[0].Tags[0] != "popular" {
		t.Fatalf("top tags=%v", examples[0].Tags)
	}
	for _, tag := range examples[0].Tags {
		if tag == "only-old" || tag == "tag-32" {
			t.Fatalf("tag outside recent-100/top-32 window included: %q", tag)
		}
	}
}

func TestRecommendationRegressionExamplesLimitEachStatusAndReturnNotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	insertTrainingRun(t, s, "train", `{}`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2002; i++ {
		status := "approved"
		if i > 1001 {
			status = "rejected"
		}
		artistID, artworkID := 10000+i, 20000+i
		if _, err = tx.ExecContext(ctx, `INSERT INTO artists(id,review_status,created_at,updated_at) VALUES(?,?,1,1)`, artistID, status); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artworks(id,artist_id,page_count,created_at,updated_at) VALUES(?,?,1,1,1)`, artworkID, artistID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artwork_tags(illust_id,tag_id) SELECT ?,id FROM tags WHERE normalized_name='bulk'`, artworkID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO tags(normalized_name,display_name) VALUES('bulk','bulk')`); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2002; i++ {
		if _, err = tx.ExecContext(ctx, `INSERT INTO artwork_tags(illust_id,tag_id) SELECT ?,id FROM tags WHERE normalized_name='bulk'`, 20000+i); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	examples, err := s.RecommendationRegressionExamples(ctx, "train")
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 2000 {
		t.Fatalf("example count=%d, want 2000", len(examples))
	}
	positives, negatives := 0, 0
	for _, example := range examples {
		if example.Liked {
			positives++
		} else {
			negatives++
		}
	}
	if positives != 1000 || negatives != 1000 {
		t.Fatalf("class counts = %d positives / %d negatives", positives, negatives)
	}
	if _, err = s.RecommendationRegressionExamples(ctx, "missing"); err != domain.ErrNotFound {
		t.Fatalf("missing run error=%v", err)
	}
}

func insertTrainingRun(t *testing.T, s *Store, id, budget string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at) VALUES(?,'succeeded','v1',?,1)`, id, budget); err != nil {
		t.Fatal(err)
	}
}

func seedTrainingArtist(t *testing.T, s *Store, id int64, status string) {
	t.Helper()
	if err := s.UpsertArtist(context.Background(), domain.Artist{ID: id, Name: fmt.Sprintf("artist-%d", id)}, "manual", "training-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE artists SET review_status=? WHERE id=?`, status, id); err != nil {
		t.Fatal(err)
	}
}

func seedTrainingArtwork(t *testing.T, s *Store, artistID, artworkID int64, tags []string, publishedAt int64) {
	t.Helper()
	if err := s.UpsertArtwork(context.Background(), domain.Artwork{
		ID: artworkID, ArtistID: artistID, PageCount: 1, MetadataState: "complete", Tags: tags,
		PublishedAt: &publishedAt,
		Pages:       []domain.Page{{Index: 0, OriginalURL: fmt.Sprintf("https://i.pximg.net/%d.jpg", artworkID), Width: 10, Height: 10}},
	}); err != nil {
		t.Fatal(err)
	}
}

func insertTrainingSources(t *testing.T, s *Store, runID string, artistID int64, sources []string) {
	t.Helper()
	raw, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,created_at,updated_at) VALUES(?,?,0,'[]','[]',?,1,1)`, runID, artistID, string(raw)); err != nil {
		t.Fatal(err)
	}
}
