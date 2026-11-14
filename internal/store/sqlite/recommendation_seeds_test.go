package sqlite

import (
	"context"
	"encoding/json"
	"math"
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
	if len(first) != 8 || !seen[1] || !seen[2] {
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

func TestRecommendationSeedsFreezeRecentApprovalsAndLongTermContentRatio(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	// Four non-recent classified artists form a balanced library. One extra
	// recent approval must be recorded but should not displace them.
	for id := int64(100); id <= 101; id++ {
		seedEligibleArtist(t, s, id, "approved")
		seedSelectionArtwork(t, s, id+1000, id, "r18", "non_ai", nil, 1)
	}
	for id := int64(102); id <= 103; id++ {
		seedEligibleArtist(t, s, id, "approved")
		seedSelectionArtwork(t, s, id+1000, id, "safe", "non_ai", nil, 1)
	}
	seedEligibleArtist(t, s, 104, "approved")
	seedSelectionArtwork(t, s, 1104, 104, "r18g", "non_ai", nil, 1)
	seedEligibleArtist(t, s, 105, "blocked")
	seedSelectionArtwork(t, s, 1105, 105, "safe", "non_ai", nil, 1)
	// Unknown-only artists are not counted as safe in the library ratio.
	seedEligibleArtist(t, s, 106, "approved")
	seedSelectionArtwork(t, s, 1106, 106, "unknown", "non_ai", nil, 1)
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at,finished_at) VALUES('prev','succeeded','v1','{}',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{104, 105} {
		if _, err := s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES('prev',?,.5,'[]','[]','[]','low','v1','{}',1,1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	run, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"seed_artists":4}`))
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT budget_json FROM recommendation_runs WHERE id=?`, run).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var budget struct {
		Recent []int64 `json:"recent_approved_artist_ids"`
		Ratio  float64 `json:"library_adult_ratio"`
		Seeds  []int64 `json:"seed_artist_ids"`
	}
	if err = json.Unmarshal([]byte(raw), &budget); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(budget.Recent, []int64{104}) {
		t.Fatalf("recent approved IDs=%v", budget.Recent)
	}
	// The newly approved adult artist counts .2, versus 1 for each established
	// artist: (two established adult + .2) / (four established + .2).
	if math.Abs(budget.Ratio-2.2/4.2) > 0.0001 {
		var debug string
		_ = s.db.QueryRow(`SELECT group_concat(artist_id||':'||rating) FROM artworks`).Scan(&debug)
		t.Fatalf("library adult ratio=%v, want %v; artworks=%s", budget.Ratio, 2.2/4.2, debug)
	}
	if len(budget.Seeds) != 4 {
		t.Fatalf("frozen seeds=%v", budget.Seeds)
	}
	var adult, safe, recent, blocked int
	for _, id := range budget.Seeds {
		if id == 104 {
			recent++
		}
		if id == 105 {
			blocked++
		}
		var rating string
		_ = s.db.QueryRow(`SELECT rating FROM artworks WHERE artist_id=? ORDER BY id LIMIT 1`, id).Scan(&rating)
		if rating == "r18" || rating == "r18g" {
			adult++
		}
		if rating == "safe" {
			safe++
		}
	}
	if recent > 1 || blocked != 0 || adult != 2 || safe != 2 {
		t.Fatalf("seed distribution adult=%d safe=%d recent=%d blocked=%d seeds=%v", adult, safe, recent, blocked, budget.Seeds)
	}
}
