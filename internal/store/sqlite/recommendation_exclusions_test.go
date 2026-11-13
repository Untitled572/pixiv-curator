package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func openExclusionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "recommendation-exclusions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insertExclusionRun(t *testing.T, s *Store, runID string, started int64, ids []int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at,finished_at) VALUES(?,'succeeded','v1','{}',?,?)`, runID, started, now); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if _, err := s.db.Exec(`INSERT INTO artists(id,name,account,review_status,created_at,updated_at) VALUES(?, 'prior candidate','prior','pending',?,?)`, id, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,?,'["old"]','["12345"]','["old-source"]','high','v1','{"old":true}',?,?)`, runID, id, float64(i+1)/100, now, now); err != nil {
			t.Fatal(err)
		}
	}
}

func readExcludedIDs(t *testing.T, s *Store, runID string) []int64 {
	t.Helper()
	ids, err := s.RecommendationExcludedArtistIDs(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestStartRecommendationFreezesOnlyImmediatelyPreviousRunExclusions(t *testing.T) {
	s := openExclusionStore(t)
	now := time.Now().UnixMilli()
	insertExclusionRun(t, s, "older", now-2000, []int64{701, 702})
	insertExclusionRun(t, s, "latest", now-1000, []int64{703, 704})
	runID, _, err := s.StartRecommendation(context.Background(), json.RawMessage(`{"candidate_artists":30}`))
	if err != nil {
		t.Fatal(err)
	}
	got := readExcludedIDs(t, s, runID)
	if len(got) != 2 || got[0] != 703 || got[1] != 704 {
		t.Fatalf("frozen exclusions=%v, want [703 704]", got)
	}
	if _, err = s.db.Exec(`UPDATE recommendation_runs SET state='succeeded',finished_at=? WHERE id=?`, time.Now().UnixMilli(), "latest"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE recommendation_runs SET started_at=? WHERE id=?`, now+1000, "older"); err != nil {
		t.Fatal(err)
	}
	if got = readExcludedIDs(t, s, runID); len(got) != 2 || got[0] != 703 || got[1] != 704 {
		t.Fatalf("retry-time exclusions drifted: %v", got)
	}
}

func TestRecommendationRecallSkipsFrozenIDsBeforeArtistAndSourceWrites(t *testing.T) {
	s := openExclusionStore(t)
	now := time.Now().UnixMilli()
	insertExclusionRun(t, s, "older", now-2000, []int64{710})
	insertExclusionRun(t, s, "latest", now-1000, []int64{720})
	runID, _, err := s.StartRecommendation(context.Background(), json.RawMessage(`{"candidate_artists":30}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[int64]domain.Artist{
		710: {ID: 710, Name: "older run is allowed"},
		720: {ID: 720, Name: "latest run excluded"},
		730: {ID: 730, Name: "new candidate"},
	}
	sources := map[int64][]string{710: {"from-old"}, 720: {"from-latest"}, 730: {"from-new"}}
	if err = s.CommitRecommendationRecall(context.Background(), job, runID, 0, candidates, sources, 30); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitRecommendationRecall(context.Background(), job, runID, 0, candidates, sources, 30); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT artist_id FROM recommendations WHERE run_id=? ORDER BY artist_id`, runID)
	if err != nil {
		t.Fatal(err)
	}
	got := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 710 || got[1] != 730 {
		t.Fatalf("recall rows=%v, want [710 730]", got)
	}
	var excludedArtist, excludedSource int
	if err = s.db.QueryRow(`SELECT count(*) FROM artists WHERE id=720`).Scan(&excludedArtist); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM artist_sources WHERE artist_id=720`).Scan(&excludedSource); err != nil {
		t.Fatal(err)
	}
	if excludedArtist != 1 || excludedSource != 0 {
		t.Fatalf("excluded artist/source writes = %d/%d, want existing artist and no source", excludedArtist, excludedSource)
	}
	var rowCount int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendations WHERE run_id=? AND artist_id=730`, runID).Scan(&rowCount); err != nil || rowCount != 1 {
		t.Fatalf("replayed candidate count=%d err=%v", rowCount, err)
	}
}

func TestRecommendationExclusionsAreEmptyWithoutPreviousSuccess(t *testing.T) {
	s := openExclusionStore(t)
	runID, _, err := s.StartRecommendation(context.Background(), json.RawMessage(`{"candidate_artists":30}`))
	if err != nil {
		t.Fatal(err)
	}
	got := readExcludedIDs(t, s, runID)
	if len(got) != 0 {
		t.Fatalf("exclusions without prior run=%v", got)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["excluded_artist_ids"]) != "[]" {
		t.Fatalf("empty exclusion snapshot=%s", fields["excluded_artist_ids"])
	}
}

func TestRefillExclusionsKeepCarryoversAndSkipTheirRecallReplay(t *testing.T) {
	s := openExclusionStore(t)
	now := time.Now().UnixMilli()
	oldIDs := make([]int64, 15)
	for i := range oldIDs {
		oldIDs[i] = int64(800000 + i)
	}
	insertExclusionRun(t, s, "parent", now-1000, oldIDs)
	// Schedule and claim the existing coordinator using the same durable queue contract.
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = scheduleRecommendationRefill(context.Background(), tx, oldIDs[0]); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE jobs SET available_at=0 WHERE dedupe_key='recommendation-refill' AND state='pending'`); err != nil {
		t.Fatal(err)
	}
	coordinator, err := s.Claim(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartRecommendationRefill(context.Background(), coordinator, json.RawMessage(`{"seed_artists":8,"candidate_artists":30,"recall_artists":60,"exploration_artists":6,"total_requests":80}`)); err != nil {
		t.Fatal(err)
	}
	runID, err := s.LatestRecommendation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	excluded := readExcludedIDs(t, s, runID)
	if len(excluded) != 15 || excluded[0] != oldIDs[0] || excluded[14] != oldIDs[14] {
		t.Fatalf("refill exclusion snapshot=%v", excluded)
	}
	job, err := s.Claim(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[int64]domain.Artist{777777: {ID: 777777, Name: "new"}}
	sources := map[int64][]string{777777: {"new-source"}}
	for _, id := range oldIDs {
		candidates[id] = domain.Artist{ID: id, Name: "old replay"}
		sources[id] = []string{"replayed-source"}
	}
	if err = s.CommitRecommendationRecall(context.Background(), job, runID, 0, candidates, sources, 45); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM recommendations WHERE run_id=?`, runID).Scan(&count); err != nil || count != 16 {
		t.Fatalf("refill row count=%d err=%v, want 15 carryover + 1 new", count, err)
	}
	var score float64
	var sampleIDs, components string
	if err = s.db.QueryRow(`SELECT score,sample_illust_ids_json,components_json FROM recommendations WHERE run_id=? AND artist_id=?`, runID, oldIDs[0]).Scan(&score, &sampleIDs, &components); err != nil {
		t.Fatal(err)
	}
	if score != 0.01 || sampleIDs != `["12345"]` || !json.Valid([]byte(components)) {
		t.Fatalf("carryover changed score=%v samples=%s components=%s", score, sampleIDs, components)
	}
	var replaySource int
	if err = s.db.QueryRow(`SELECT count(*) FROM artist_sources WHERE artist_id=? AND source_key LIKE '%replayed-source%'`, oldIDs[0]).Scan(&replaySource); err != nil || replaySource != 0 {
		t.Fatalf("carryover replay source count=%d err=%v", replaySource, err)
	}
}
