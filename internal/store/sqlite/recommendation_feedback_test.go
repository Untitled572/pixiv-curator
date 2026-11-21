package sqlite

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestRecommendationFeedbackSettlesOnlyOnSuccessAndClearsOnDecision(t *testing.T) {
	ctx := context.Background()
	s := openExclusionStore(t)
	now := time.Now()
	insertExclusionRun(t, s, "parent", now.Add(-time.Minute).UnixMilli(), []int64{901, 902})
	seedSelectionArtwork(t, s, 12345, 901, "safe", "non_ai", []string{"sample-tag"}, 1)
	budget := json.RawMessage(`{"candidate_artists":15,"weak_feedback_enabled":true,"exposure_parent":"parent","exposure_ids":[901,902]}`)
	run, _, err := s.StartRecommendation(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendation_weak_skips`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("before completion %d %v", n, err)
	}
	if _, err = s.db.Exec(`UPDATE artists SET review_status='rejected' WHERE id=902`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = settleRecommendationExposure(ctx, tx, run); err != nil {
		t.Fatal(err)
	}
	if err = settleRecommendationExposure(ctx, tx, run); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendation_weak_skips`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("settled %d %v", n, err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := recommendationSkipWeight(ctx, tx, 901, time.Now())
	if err != nil || math.Abs(w-0.7) > 0.001 {
		t.Fatalf("weight %v %v", w, err)
	}
	if err = recordRecommendationDecision(ctx, tx, 901, "approve"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendation_weak_skips`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after decision %d %v", n, err)
	}
	var snapshot string
	if err = s.db.QueryRow(`SELECT feature_json FROM recommendation_decisions WHERE artist_id=901`).Scan(&snapshot); err != nil || snapshot == "[]" {
		t.Fatalf("snapshot %s %v", snapshot, err)
	}
}

func TestRecommendationFeedbackRejectsStaleAndForeignExposures(t *testing.T) {
	ctx := context.Background()
	s := openExclusionStore(t)
	now := time.Now().UnixMilli()
	insertExclusionRun(t, s, "older", now-2000, []int64{910})
	insertExclusionRun(t, s, "latest", now-1000, []int64{911})
	for _, raw := range []string{`{"exposure_parent":"older","exposure_ids":[910]}`, `{"exposure_parent":"latest","exposure_ids":[910]}`, `{"exposure_parent":"latest","exposure_ids":[911,911]}`} {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = validateRecommendationExposure(ctx, tx, []byte(raw))
		tx.Rollback()
		if err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestRecommendationWeakSkipDecaysAndExpires(t *testing.T) {
	ctx := context.Background()
	s := openExclusionStore(t)
	now := time.Now()
	insertExclusionRun(t, s, "decay", now.Add(-time.Hour).UnixMilli(), []int64{920})
	if _, err := s.db.Exec(`INSERT INTO recommendation_weak_skips(run_id,artist_id,feature_json,skipped_at) VALUES('decay',920,'[]',?)`, now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	midway, err := recommendationSkipWeight(ctx, tx, 920, now.Add(22*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := recommendationSkipWeight(ctx, tx, 920, now.Add(46*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if midway <= 0.7 || midway >= 1 || expired != 1 {
		t.Fatalf("midway %v expired %v", midway, expired)
	}
}
