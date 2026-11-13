package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestRecommendationProgressUsesWorkPhases(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	budget := json.RawMessage(`{"seed_artists":4,"seed_artist_ids":[2,5,8,12]}`)
	runID, _, err := s.StartRecommendation(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.RecommendationRun{ID: runID, State: "pending", Budget: budget}
	assertProgress := func(want int) {
		t.Helper()
		got, err := s.recommendationProgress(ctx, run)
		if err != nil {
			t.Fatalf("recommendationProgress() error = %v", err)
		}
		if got != want {
			t.Fatalf("recommendationProgress() = %d, want %d", got, want)
		}
	}

	assertProgress(0)
	run.State = "running"
	assertProgress(1) // The run has started but has not processed a seed.
	run.SeedCursor = 5
	assertProgress(22) // Two of the four persisted seed IDs are complete.
	run.SeedCursor = 12
	assertProgress(40)

	if _, err = s.db.ExecContext(ctx, `UPDATE recommendation_runs SET stats_json='{"tag_count":1}' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{101, 102, 103, 104} {
		insertArtist(t, s, id, "candidate")
		version := ""
		if i < 2 {
			version = "v1"
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,score_version,created_at,updated_at) VALUES(?,?,0,'[]','[]','[]',?,1,1)`, runID, id, version); err != nil {
			t.Fatal(err)
		}
	}
	assertProgress(67) // Two of four candidates have been scored.
	if _, err = s.db.ExecContext(ctx, `UPDATE recommendations SET score_version='v1' WHERE run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	assertProgress(95)
	run.State = "succeeded"
	assertProgress(100)
}

func TestRecommendationProgressScoringWithNoCandidatesCapsAtNinetyFive(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	budget := json.RawMessage(`{"seed_artists":1}`)
	runID, _, err := s.StartRecommendation(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE recommendation_runs SET stats_json='{"tag_count":0}' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	got, err := s.recommendationProgress(ctx, domain.RecommendationRun{ID: runID, State: "running", Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if got != 95 {
		t.Fatalf("recommendationProgress() = %d, want 95", got)
	}
}
