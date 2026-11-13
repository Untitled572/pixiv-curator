package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRecommendationSeedsIncludeHistoricalAdultInterestAndStayFrozen(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for id := int64(1); id <= 12; id++ {
		seedEligibleArtist(t, s, id, "approved")
		if _, err := s.db.Exec(`UPDATE artists SET created_at=? WHERE id=?`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{1, 2} {
		seedSelectionArtwork(t, s, id*100, id, "r18", "non_ai", nil, 1)
	}
	run, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"seed_artists":8}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.RecommendationSeedsForRun(ctx, run, 8)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, a := range first {
		seen[a.ID] = true
	}
	if len(first) != 8 || !seen[1] || !seen[2] || !seen[9] || !seen[10] || !seen[11] || !seen[12] {
		t.Fatalf("seeds=%v", seen)
	}
	seedEligibleArtist(t, s, 99, "approved")
	if _, err = s.db.Exec(`UPDATE artists SET created_at=999 WHERE id=99`); err != nil {
		t.Fatal(err)
	}
	again, err := s.RecommendationSeedsForRun(ctx, run, 8)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("resume changed frozen seeds")
	}
	if _, err = s.DecideArtist(ctx, 1, "block"); err != nil {
		t.Fatal(err)
	}
	after, err := s.RecommendationSeedsForRun(ctx, run, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after {
		if a.ID == 1 {
			t.Fatal("blocked seed retained")
		}
	}
}
