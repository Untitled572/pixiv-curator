package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func seedRefillRun(t *testing.T, s *Store, pending, decided int) (string, []int64) {
	t.Helper()
	ctx := context.Background()
	runID := randomID()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at,finished_at) VALUES(?,'succeeded','v1','{}',?,?)`, runID, now-1000, now); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, pending+decided)
	for i := 0; i < pending+decided; i++ {
		id := int64(800000 + i)
		status := "pending"
		if i >= pending {
			status = "approved"
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO artists(id,name,account,created_at,updated_at,review_status) VALUES(?,?,?,?,?,?)`, id, "candidate", "candidate", now, now, status); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,?, '["reason"]','["12345"]','["source"]','high','v1','{"raw":true}',?,?)`, runID, id, float64(i+1)/100, now, now); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return runID, ids
}

func scheduleRefillForTest(t *testing.T, s *Store, artistID int64) {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduleRecommendationRefill(context.Background(), tx, artistID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func claimRefill(t *testing.T, s *Store) domain.Job {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE jobs SET available_at=0 WHERE dedupe_key='recommendation-refill' AND state='pending'`); err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != "recommendation_refill" {
		t.Fatalf("claimed kind=%q, want recommendation_refill", j.Kind)
	}
	return j
}

func TestRecommendationRefillNoLongerSchedulesFromReviewOrActivity(t *testing.T) {
	s := openRecommendationStore(t)
	_, ids := seedRefillRun(t, s, 15, 1)
	for _, id := range ids[:2] {
		scheduleRefillForTest(t, s, id)
		if err := s.TouchRecommendationActivity(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	assertNoQueuedRefill(t, s)
}

func TestLegacyRefillCoordinatorDoesNotCreateOrCopyRecommendations(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationStore(t)
	oldRun, ids := seedRefillRun(t, s, 15, 2)
	seedLegacyRefillJob(t, s, oldRun)
	j := claimRefill(t, s)
	if err := s.StartRecommendationRefill(ctx, j, json.RawMessage(`{"seed_artists":8,"candidate_artists":30}`)); err != nil {
		t.Fatal(err)
	}
	var runCount, retained, follows int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM recommendation_runs),
		(SELECT count(*) FROM recommendations WHERE run_id=?),
		(SELECT count(*) FROM jobs WHERE kind='follow_artist')`, oldRun).Scan(&runCount, &retained, &follows); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || retained != len(ids) || follows != 0 {
		t.Fatalf("run count=%d, retained=%d, follow jobs=%d", runCount, retained, follows)
	}
	assertRefillJobState(t, s, j.ID, "succeeded")
}

func TestPrepareRecommendationRefillRevokesRunningCoordinatorWithoutReplacement(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationStore(t)
	run, _ := seedRefillRun(t, s, 15, 0)
	seedLegacyRefillJob(t, s, run)
	j := claimRefill(t, s)
	if err := s.PrepareRecommendationRefill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchRecommendationActivity(ctx); err != nil {
		t.Fatal(err)
	}
	assertRefillJobState(t, s, j.ID, "canceled")
	assertNoQueuedRefill(t, s)
	if err := s.StartRecommendationRefill(ctx, j, json.RawMessage(`{}`)); err != domain.ErrLease {
		t.Fatalf("canceled coordinator error=%v, want ErrLease", err)
	}
}

func TestRecommendationRefillStaleRunIsNoop(t *testing.T) {
	s := openRecommendationStore(t)
	oldRun, _ := seedRefillRun(t, s, 15, 0)
	seedLegacyRefillJob(t, s, oldRun)
	j := claimRefill(t, s)
	newerRun := randomID()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at,finished_at) VALUES(?,'succeeded','v1','{}',?,?)`, newerRun, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRecommendationRefill(context.Background(), j, json.RawMessage(`{"seed_artists":8}`)); err != nil {
		t.Fatal(err)
	}
	var runCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM recommendation_runs`).Scan(&runCount); err != nil || runCount != 2 {
		t.Fatalf("runs=%d err=%v", runCount, err)
	}
	var oldState string
	if err := s.db.QueryRow(`SELECT state FROM recommendation_runs WHERE id=?`, oldRun).Scan(&oldState); err != nil || oldState != "succeeded" {
		t.Fatalf("old run state=%q err=%v", oldState, err)
	}
	var jobState string
	if err := s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, j.ID).Scan(&jobState); err != nil || jobState != "succeeded" {
		t.Fatalf("job state=%q err=%v", jobState, err)
	}
}

func TestPrepareRecommendationRefillCancelsPersistedJobOnRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "refill-restart.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	oldRun, _ := seedRefillRun(t, s, 12, 0)
	id := seedLegacyRefillJob(t, s, oldRun)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.PrepareRecommendationRefill(ctx); err != nil {
		t.Fatal(err)
	}
	assertRefillJobState(t, s, id, "canceled")
	assertNoQueuedRefill(t, s)
	run, err := s.LatestRecommendation(ctx)
	if err != nil || run != oldRun {
		t.Fatalf("run=%q previous=%q err=%v", run, oldRun, err)
	}
}

func TestRecommendationRefillFinalizationCannotPublishOldCarryover(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationStore(t)
	parent, ids := seedRefillRun(t, s, 15, 0)
	run, job := seedLegacyRefillWork(t, s, parent)
	if err := s.CompleteRecommendation(ctx, job, run); err != domain.ErrLease {
		t.Fatalf("obsolete refill finalization error=%v, want ErrLease", err)
	}
	if err := s.PrepareRecommendationRefill(ctx); err != nil {
		t.Fatal(err)
	}
	assertRefillJobState(t, s, job.ID, "canceled")
	var state, samples string
	if err := s.db.QueryRow("SELECT state FROM recommendation_runs WHERE id=?", run).Scan(&state); err != nil || state != "canceled" {
		t.Fatalf("obsolete run state=%q err=%v", state, err)
	}
	if err := s.db.QueryRow("SELECT sample_illust_ids_json FROM recommendations WHERE run_id=? AND artist_id=?", parent, ids[0]).Scan(&samples); err != nil || samples != `["12345"]` {
		t.Fatalf("parent samples=%q err=%v", samples, err)
	}
}

func TestRecommendationSingleVacancyStillDoesNotScheduleRefill(t *testing.T) {
	s := openRecommendationStore(t)
	_, ids := seedRefillRun(t, s, 14, 1)
	scheduleRefillForTest(t, s, ids[0])
	assertNoQueuedRefill(t, s)
}

func TestRecommendationDecisionsDoNotScheduleRefill(t *testing.T) {
	for _, action := range []string{"approve", "reject"} {
		t.Run(action, func(t *testing.T) {
			s := openRecommendationStore(t)
			_, ids := seedRefillRun(t, s, 15, 0)
			if _, err := s.DecideArtist(context.Background(), ids[0], action); err != nil {
				t.Fatal(err)
			}
			assertNoQueuedRefill(t, s)
		})
	}
}

func mustJSONStrings(t *testing.T, id int64) string {
	t.Helper()
	data, err := json.Marshal([]string{jsonInt64(id)})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func jsonInt64(id int64) string { return strconv.FormatInt(id, 10) }

func TestRecommendationRefillRecallCannotAdmitObsoleteCohort(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationStore(t)
	parent, _ := seedRefillRun(t, s, 15, 2)
	run, job := seedLegacyRefillWork(t, s, parent)
	candidate := domain.Artist{ID: 950000}
	err := s.CommitRecommendationRecall(ctx, job, run, 0, map[int64]domain.Artist{candidate.ID: candidate}, nil, 45)
	if err != domain.ErrLease {
		t.Fatalf("recall error=%v, want ErrLease", err)
	}
	var total int
	if err = s.db.QueryRow("SELECT count(*) FROM recommendations WHERE run_id=?", run).Scan(&total); err != nil || total != 0 {
		t.Fatalf("obsolete candidates=%d err=%v", total, err)
	}
}

func assertNoQueuedRefill(t *testing.T, s *Store) {
	t.Helper()
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind='recommendation_refill' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("active refill jobs=%d err=%v", count, err)
	}
}

func assertRefillJobState(t *testing.T, s *Store, id, want string) {
	t.Helper()
	var state string
	if err := s.db.QueryRow("SELECT state FROM jobs WHERE id=?", id).Scan(&state); err != nil || state != want {
		t.Fatalf("refill state=%q want=%q err=%v", state, want, err)
	}
}

func seedLegacyRefillJob(t *testing.T, s *Store, parent string) string {
	t.Helper()
	payload, err := json.Marshal(recommendationRefillPayload{RunID: parent})
	if err != nil {
		t.Fatal(err)
	}
	id := randomID()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at)
		VALUES(?,'recommendation_refill','recommendation-refill',?,'pending',0,?,?)`, id, string(payload), now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedLegacyRefillWork(t *testing.T, s *Store, parent string) (string, domain.Job) {
	t.Helper()
	ctx := context.Background()
	run, _, err := s.StartRecommendation(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE recommendation_runs SET budget_json=json_set(budget_json,'$.refill_parent_run',?) WHERE id=?`, parent, run); err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(ctx, time.Now())
	if err != nil || j.Kind != "recommend_artists" {
		t.Fatalf("claim kind=%q err=%v", j.Kind, err)
	}
	return run, j
}
