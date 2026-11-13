package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"reflect"
	"testing"
)

func TestExplorationSelectionPersistsSixOutsideTopTwentyFour(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, run, j := runningRecommendation(t, s)
	if _, err := s.db.Exec(`UPDATE recommendation_runs SET budget_json='{"candidate_artists":30,"exploration_artists":6}' WHERE id=?`, run); err != nil {
		t.Fatal(err)
	}
	candidates := map[int64]domain.Artist{}
	for id := int64(1); id <= 60; id++ {
		candidates[id] = domain.Artist{ID: id}
	}
	if err := s.CommitRecommendationRecall(ctx, j, run, 0, candidates, nil, 60); err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 60; id++ {
		a := domain.Artwork{ID: id * 10, ArtistID: id, Kind: "illust", Rating: "safe", AIStatus: "non_ai", MetadataState: "complete", Visible: true, PageCount: 1, Pages: []domain.Page{{Index: 0, State: "indexed", OriginalURL: "https://i.pximg.net/img-original/test.jpg"}}}
		r := domain.Recommendation{ArtistID: id, Score: float64(61-id) / 60, SampleIDs: []string{fmt.Sprint(a.ID)}, Components: json.RawMessage(`{}`)}
		if err := s.CommitRecommendationSamples(ctx, j, run, id, id, []domain.Artwork{a}, r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DecideArtist(ctx, 60, "block"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRecommendation(ctx, j, run); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecommendationCandidates(ctx, run, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 30 {
		t.Fatalf("count=%d", len(items))
	}
	random := 0
	seen := map[int64]bool{}
	for _, item := range items {
		if seen[item.ArtistID] || item.ArtistID == 60 || item.Artist.ReviewStatus != "pending" {
			t.Fatalf("invalid candidate %+v", item)
		}
		seen[item.ArtistID] = true
		if item.Exploration {
			random++
			if item.ArtistID <= 24 {
				t.Fatal("exploration overlaps top24")
			}
		} else if item.ArtistID > 24 {
			t.Fatal("normal lost ranking")
		}
	}
	if random != 6 {
		t.Fatalf("random=%d", random)
	}
	again, err := s.RecommendationCandidates(ctx, run, 0)
	if err != nil || !reflect.DeepEqual(items, again) {
		t.Fatal("read rerolled selection")
	}
}
func TestRepresentativeSamplesPreferPopularityAndOneRecent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 900, "approved")
	for _, id := range []int64{901, 902, 903, 904} {
		seedSelectionArtwork(t, s, id, 900, "safe", "non_ai", nil, 1)
	}
	if _, err := s.db.Exec(`UPDATE artworks SET bookmarks=CASE id WHEN 901 THEN 900 WHEN 902 THEN 800 WHEN 903 THEN 1 ELSE 999 END,published_at=CASE id WHEN 903 THEN 100 ELSE 1 END WHERE artist_id=900; INSERT INTO artwork_preferences VALUES(904,'dislike',0)`); err != nil {
		t.Fatal(err)
	}
	works, err := s.RecommendationSamples(ctx, 900, 3)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, a := range works {
		ids = append(ids, a.ID)
	}
	if !reflect.DeepEqual(ids, []int64{901, 902, 903}) {
		t.Fatalf("samples=%v", ids)
	}
}
