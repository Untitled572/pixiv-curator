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

func TestRecommendationRefillSchedulesAndDebounces(t *testing.T) {
	s := openRecommendationStore(t)
	_, ids := seedRefillRun(t, s, 15, 1)
	scheduleRefillForTest(t, s, ids[0])
	var firstID string
	var firstAvailable int64
	if err := s.db.QueryRow(`SELECT id,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&firstID, &firstAvailable); err != nil {
		t.Fatal(err)
	}
	if delta := time.Until(time.UnixMilli(firstAvailable)); delta < 4*time.Minute || delta > 5*time.Minute+time.Second {
		t.Fatalf("initial deadline delta=%s", delta)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET available_at=0 WHERE id=?`, firstID); err != nil {
		t.Fatal(err)
	}
	scheduleRefillForTest(t, s, ids[1])
	var id string
	var secondAvailable int64
	if err := s.db.QueryRow(`SELECT id,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&id, &secondAvailable); err != nil {
		t.Fatal(err)
	}
	if id != firstID {
		t.Fatalf("reschedule changed job or failed to debounce: id %q -> %q, available %d -> %d", firstID, id, firstAvailable, secondAvailable)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET available_at=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchRecommendationActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var touchedAvailable int64
	if err := s.db.QueryRow(`SELECT available_at FROM jobs WHERE id=?`, id).Scan(&touchedAvailable); err != nil {
		t.Fatal(err)
	}
	if touchedAvailable < time.Now().Add(4*time.Minute).UnixMilli() {
		t.Fatalf("activity did not extend deadline: %d", touchedAvailable)
	}
}

func TestRecommendationRefillCarriesPendingRowsAndBoundsNewBudget(t *testing.T) {
	s := openRecommendationStore(t)
	oldRun, ids := seedRefillRun(t, s, 15, 2)
	// One coordinator is scheduled from a decision on the previous run.
	scheduleRefillForTest(t, s, ids[0])
	j := claimRefill(t, s)
	if err := s.StartRecommendationRefill(context.Background(), j, json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"recall_artists":60,"exploration_artists":6,"total_requests":80}`)); err != nil {
		t.Fatal(err)
	}
	newRun, err := s.LatestRecommendation(context.Background())
	if err != nil || newRun == oldRun {
		t.Fatalf("LatestRecommendation() = %q, %v; old=%q", newRun, err, oldRun)
	}
	var budget string
	if err = s.db.QueryRow(`SELECT budget_json FROM recommendation_runs WHERE id=?`, newRun).Scan(&budget); err != nil {
		t.Fatal(err)
	}
	var got struct {
		CandidateArtists   int `json:"candidate_artists"`
		RecallArtists      int `json:"recall_artists"`
		ExplorationArtists int `json:"exploration_artists"`
		TotalRequests      int `json:"total_requests"`
	}
	if err = json.Unmarshal([]byte(budget), &got); err != nil {
		t.Fatal(err)
	}
	if got.CandidateArtists != 15 || got.RecallArtists != 45 || got.ExplorationArtists != 3 || got.TotalRequests != 80 {
		t.Fatalf("refill budget = %#v", got)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendations WHERE run_id=?`, newRun).Scan(&n); err != nil || n != 15 {
		t.Fatalf("carried candidates=%d, err=%v; want 15", n, err)
	}
	for _, id := range ids[:15] {
		var score float64
		var reasons, samples, sources, components string
		if err = s.db.QueryRow(`SELECT score,reasons_json,sample_illust_ids_json,sources_json,components_json FROM recommendations WHERE run_id=? AND artist_id=?`, newRun, id).Scan(&score, &reasons, &samples, &sources, &components); err != nil {
			t.Fatal(err)
		}
		var originalScore float64
		if err = s.db.QueryRow(`SELECT score FROM recommendations WHERE run_id=? AND artist_id=?`, oldRun, id).Scan(&originalScore); err != nil {
			t.Fatal(err)
		}
		if score != originalScore || reasons != `["reason"]` || samples != `["12345"]` || sources != `["source"]` {
			t.Fatalf("candidate %d metadata changed: score=%v reasons=%s samples=%s sources=%s", id, score, reasons, samples, sources)
		}
		var flags map[string]any
		if err = json.Unmarshal([]byte(components), &flags); err != nil || flags["carryover"] != true || flags["raw"] != true {
			t.Fatalf("candidate %d components=%s err=%v", id, components, err)
		}
	}
	var countApproved int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendations WHERE run_id=? AND artist_id=?`, newRun, ids[15]).Scan(&countApproved); err != nil || countApproved != 0 {
		t.Fatalf("approved candidate carried: count=%d err=%v", countApproved, err)
	}
	var jobState string
	if err = s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, j.ID).Scan(&jobState); err != nil || jobState != "succeeded" {
		t.Fatalf("coordinator state=%q err=%v", jobState, err)
	}
	var followJobs int
	if err = s.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind='follow_artist'`).Scan(&followJobs); err != nil || followJobs != 0 {
		t.Fatalf("refill created follow jobs=%d err=%v", followJobs, err)
	}
}

func TestRecommendationRefillActivityRevokesRunningCoordinator(t *testing.T) {
	s := openRecommendationStore(t)
	_, ids := seedRefillRun(t, s, 15, 0)
	scheduleRefillForTest(t, s, ids[0])
	j := claimRefill(t, s)
	if err := s.TouchRecommendationActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, j.ID).Scan(&state); err != nil || state != "canceled" {
		t.Fatalf("old coordinator state=%q err=%v", state, err)
	}
	var replacement string
	var due int64
	if err := s.db.QueryRow(`SELECT id,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&replacement, &due); err != nil {
		t.Fatal(err)
	}
	if replacement == j.ID || due < time.Now().Add(4*time.Minute).UnixMilli() {
		t.Fatalf("replacement id=%q due=%d", replacement, due)
	}
	if err := s.StartRecommendationRefill(context.Background(), j, json.RawMessage(`{"seed_artists":8}`)); err != domain.ErrLease {
		t.Fatalf("stale coordinator StartRecommendationRefill() error=%v, want ErrLease", err)
	}
}

func TestRecommendationRefillStaleRunIsNoop(t *testing.T) {
	s := openRecommendationStore(t)
	oldRun, ids := seedRefillRun(t, s, 15, 0)
	scheduleRefillForTest(t, s, ids[0])
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

func TestPrepareRecommendationRefillRestoresWithoutResettingDeadline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "refill-restart.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, ids := seedRefillRun(t, s, 12, 0)
	scheduleRefillForTest(t, s, ids[0])
	var beforeID string
	var beforeDue int64
	if err = s.db.QueryRow(`SELECT id,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&beforeID, &beforeDue); err != nil {
		t.Fatal(err)
	}
	var accountsBefore int
	if err = s.db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&accountsBefore); err != nil {
		t.Fatal(err)
	}
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
	var afterID string
	var afterDue int64
	if err = s.db.QueryRow(`SELECT id,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&afterID, &afterDue); err != nil {
		t.Fatal(err)
	}
	if afterID != beforeID || afterDue != beforeDue {
		t.Fatalf("bootstrap reset existing job: id %q -> %q due %d -> %d", beforeID, afterID, beforeDue, afterDue)
	}
	var accountsAfter int
	if err = s.db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&accountsAfter); err != nil || accountsAfter != accountsBefore {
		t.Fatalf("startup changed accounts: before=%d after=%d err=%v", accountsBefore, accountsAfter, err)
	}
}

func TestRecommendationRefillFinalizationPreservesCarryoverAndSelectsNewCohort(t *testing.T) {
	s := openRecommendationStore(t)
	oldRun, ids := seedRefillRun(t, s, 15, 0)
	scheduleRefillForTest(t, s, ids[0])
	coordinator := claimRefill(t, s)
	budget := json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"recall_artists":60,"exploration_artists":6,"total_requests":80}`)
	if err := s.StartRecommendationRefill(context.Background(), coordinator, budget); err != nil {
		t.Fatal(err)
	}
	newRun, err := s.LatestRecommendation(context.Background())
	if err != nil || newRun == oldRun {
		t.Fatalf("LatestRecommendation()=%q err=%v", newRun, err)
	}
	work, err := s.Claim(context.Background(), time.Now())
	if err != nil || work.Kind != "recommend_artists" {
		t.Fatalf("claim refill work: kind=%q err=%v", work.Kind, err)
	}
	now := time.Now().UnixMilli()
	for i := int64(0); i < 15; i++ {
		artistID := int64(900000) + i
		illustID := int64(950000) + i
		if _, err = s.db.Exec(`INSERT INTO artists(id,name,account,review_status,created_at,updated_at) VALUES(?, 'new candidate','new','pending',?,?)`, artistID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`INSERT INTO artworks(id,artist_id,title,kind,page_count,rating,ai_status,metadata_state,visible,created_at,updated_at) VALUES(?,?,'sample','illust',1,'safe','non_ai','complete',1,?,?)`, illustID, artistID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(?,0,'https://i.pximg.net/img-original/sample.jpg',100,100,'indexed',?,?)`, illustID, now, now); err != nil {
			t.Fatal(err)
		}
		components := json.RawMessage(`{"score":1}`)
		if _, err = s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,?,'["new"]',?, '["refill"]','high','v1',?,?,?)`, newRun, artistID, float64(100-i)/100, mustJSONStrings(t, illustID), string(components), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CompleteRecommendation(context.Background(), work, newRun); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecommendationCandidates(context.Background(), newRun, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 30 {
		t.Fatalf("final candidates=%d, want 30", len(items))
	}
	carryCount, normalCount, exploreCount := 0, 0, 0
	for _, item := range items {
		switch item.ArtistID {
		case ids[0], ids[1], ids[2], ids[3], ids[4], ids[5], ids[6], ids[7], ids[8], ids[9], ids[10], ids[11], ids[12], ids[13], ids[14]:
			carryCount++
			if item.Exploration || item.Score != float64(item.ArtistID-800000+1)/100 || len(item.SampleIDs) != 1 || item.SampleIDs[0] != "12345" {
				t.Fatalf("carryover metadata changed: %+v", item)
			}
		default:
			if item.Exploration {
				exploreCount++
			} else {
				normalCount++
			}
		}
	}
	if carryCount != 15 || normalCount != 12 || exploreCount != 3 {
		t.Fatalf("cohorts carry=%d normal=%d exploration=%d", carryCount, normalCount, exploreCount)
	}
}

func TestRecommendationRefillBudgetDisablesTinyExplorationCohort(t *testing.T) {
	s := openRecommendationStore(t)
	oldRun, ids := seedRefillRun(t, s, 29, 1)
	scheduleRefillForTest(t, s, ids[0])
	coordinator := claimRefill(t, s)
	if err := s.StartRecommendationRefill(context.Background(), coordinator, json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"exploration_artists":6,"total_requests":80}`)); err != nil {
		t.Fatal(err)
	}
	newRun, err := s.LatestRecommendation(context.Background())
	if err != nil || newRun == oldRun {
		t.Fatalf("LatestRecommendation()=%q err=%v", newRun, err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT budget_json FROM recommendation_runs WHERE id=?`, newRun).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var b struct {
		CandidateArtists   int `json:"candidate_artists"`
		ExplorationArtists int `json:"exploration_artists"`
	}
	if err = json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	if b.CandidateArtists != 1 || b.ExplorationArtists != 0 {
		t.Fatalf("tiny refill budget=%+v", b)
	}
}

func TestRecommendationRefillCompletionSchedulesOnlyHandledCarryover(t *testing.T) {
	for _, handled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary_shortage_does_not_loop", true: "handled_carryover_schedules_once"}[handled], func(t *testing.T) {
			s := openRecommendationStore(t)
			oldRun, ids := seedRefillRun(t, s, 15, 0)
			scheduleRefillForTest(t, s, ids[0])
			coordinator := claimRefill(t, s)
			if err := s.StartRecommendationRefill(context.Background(), coordinator, json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"exploration_artists":6,"total_requests":80}`)); err != nil {
				t.Fatal(err)
			}
			newRun, err := s.LatestRecommendation(context.Background())
			if err != nil || newRun == oldRun {
				t.Fatalf("LatestRecommendation()=%q err=%v", newRun, err)
			}
			work, err := s.Claim(context.Background(), time.Now())
			if err != nil || work.Kind != "recommend_artists" {
				t.Fatalf("claim recommendation work: kind=%q err=%v", work.Kind, err)
			}
			if handled {
				if _, err = s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=?`, ids[0]); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.CompleteRecommendation(context.Background(), work, newRun); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = s.db.QueryRow(`SELECT count(*) FROM jobs WHERE dedupe_key='recommendation-refill' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if handled && count != 1 {
				t.Fatalf("handled carryover scheduled %d coordinators, want 1", count)
			}
			if !handled && count != 0 {
				t.Fatalf("ordinary shortage scheduled %d coordinators, want 0", count)
			}
			if handled {
				var payload string
				var due int64
				if err = s.db.QueryRow(`SELECT payload_json,available_at FROM jobs WHERE dedupe_key='recommendation-refill' AND state='pending'`).Scan(&payload, &due); err != nil {
					t.Fatal(err)
				}
				var p recommendationRefillPayload
				if err = json.Unmarshal([]byte(payload), &p); err != nil || p.RunID != newRun {
					t.Fatalf("scheduled payload=%s err=%v", payload, err)
				}
				if due < time.Now().Add(4*time.Minute).UnixMilli() {
					t.Fatalf("scheduled refill due too early: %d", due)
				}
			}
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

func TestRecommendationRefillRecallAdmitsNewCohortBesideCarryover(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationStore(t)
	_, ids := seedRefillRun(t, s, 15, 2)
	scheduleRefillForTest(t, s, ids[0])
	j := claimRefill(t, s)
	if err := s.StartRecommendationRefill(ctx, j, json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"recall_artists":60,"exploration_artists":6,"total_requests":80}`)); err != nil {
		t.Fatal(err)
	}
	run, err := s.LatestRecommendation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[int64]domain.Artist{}
	for id := int64(9000); id < 9060; id++ {
		candidates[id] = domain.Artist{ID: id}
	}
	if err = s.CommitRecommendationRecall(ctx, job, run, 0, candidates, nil, 45); err != nil {
		t.Fatal(err)
	}
	var total int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendations WHERE run_id=?`, run).Scan(&total); err != nil || total != 45 {
		t.Fatalf("recall pool total=%d err=%v", total, err)
	}
}
