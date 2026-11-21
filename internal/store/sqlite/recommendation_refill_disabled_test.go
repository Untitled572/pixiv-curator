package sqlite

import (
	"context"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"testing"
	"time"
)

func TestDisabledRefillRevokesQueuedAndClaimedLegacyWork(t *testing.T) {
	s := openExclusionStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	addRecommendationMixTestRun(t, s, "legacy-refill", `{"refill_parent_run":"parent"}`, `{}`)
	_, err := s.db.Exec(`INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES('refill-child','recommend_artists','old-child','{"run_id":"legacy-refill"}','pending',?,?,?)`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareRecommendationRefill(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRecommendation(ctx, job, "legacy-refill"); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("old worker completion: %v", err)
	}
	var jobState, runState string
	if err = s.db.QueryRow(`SELECT state FROM jobs WHERE id='refill-child'`).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT state FROM recommendation_runs WHERE id='legacy-refill'`).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if jobState != "canceled" || runState != "canceled" {
		t.Fatalf("job=%s run=%s", jobState, runState)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = scheduleRecommendationRefill(ctx, tx, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM jobs WHERE kind='recommendation_refill' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refill count %d: %v", count, err)
	}
}
