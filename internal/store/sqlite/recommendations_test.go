package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func openRecommendationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "recommendations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func runningRecommendation(t *testing.T, s *Store) (context.Context, string, domain.Job) {
	t.Helper()
	ctx := context.Background()
	runID, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"max_requests":2}`))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != "recommend_artists" {
		t.Fatalf("claimed kind=%s", j.Kind)
	}
	return ctx, runID, j
}

func TestRecommendationRunBudgetLeaseAndCandidateSamplesStayPending(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, j := runningRecommendation(t, s)
	ok, err := s.ReserveRecommendationRequest(ctx, j, runID, 1)
	if err != nil || !ok {
		t.Fatalf("first reservation=%v,%v", ok, err)
	}
	ok, err = s.ReserveRecommendationRequest(ctx, j, runID, 1)
	if err != nil || ok {
		t.Fatalf("over-budget reservation=%v,%v", ok, err)
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.RequestCount != 1 || run.State != "running" {
		t.Fatalf("run=%+v", run)
	}
	cand := domain.Artist{ID: 71, Name: "candidate", Account: "candidate"}
	if err = s.CommitRecommendationRecall(ctx, j, runID, 71, map[int64]domain.Artist{71: cand}, map[int64][]string{71: {"following", "shared", "shared"}}, 60); err != nil {
		t.Fatal(err)
	}
	var status string
	var enabled bool
	if err = s.db.QueryRowContext(ctx, `SELECT review_status,collection_enabled FROM artists WHERE id=71`).Scan(&status, &enabled); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || enabled {
		t.Fatalf("recommendation auto-approved artist: %q %v", status, enabled)
	}
	var sourceCount int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_sources WHERE artist_id=71 AND source_type='recommend'`).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 2 {
		t.Fatalf("recommendation source rows=%d, want 2", sourceCount)
	}
	art := domain.Artwork{ID: 9001, ArtistID: 71, Title: "sample", Kind: "illust", Rating: "safe", AIStatus: "non_ai", PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/img-original/a_p0.jpg", State: "indexed"}}}
	result := domain.Recommendation{RunID: runID, ArtistID: 71, Score: .7, Reasons: []string{"reason"}, Sources: []string{"following"}, SampleIDs: []string{"9001"}, Confidence: "medium", Version: "v1", Components: json.RawMessage(`{"popularity":0.4}`)}
	if err = s.CommitRecommendationSamples(ctx, j, runID, 71, 72, []domain.Artwork{art}, result); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ArtistID != 71 || got[0].Score != .7 || len(got[0].SampleIDs) != 1 {
		t.Fatalf("candidate result=%+v", got)
	}
	if got[0].Artist.ReviewStatus != "pending" {
		t.Fatalf("candidate status=%s", got[0].Artist.ReviewStatus)
	}
	if err = s.CommitRecommendationSamples(ctx, j, runID, 71, 60, nil, result); err != nil {
		t.Fatal(err)
	}
	run, err = s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CandidateCursor != 72 {
		t.Fatalf("candidate cursor moved backwards: %d", run.CandidateCursor)
	}
	if _, err = s.EligiblePage(ctx, art.ID, 0, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pending recommendation sample entered image selection: %v", err)
	}
	if err = s.CompleteRecommendation(ctx, j, runID); err != nil {
		t.Fatal(err)
	}
	if reused, reusedJob, e := s.StartRecommendation(ctx, json.RawMessage(`{}`)); e != nil || reused == runID || reusedJob == j.ID {
		t.Fatalf("completed run must not prevent a new run: %s %s %v", reused, reusedJob, e)
	}
}

func TestRecommendationSeedsAndSamplesRequireTrustedSeed(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	insertArtist(t, s, 1, "pending")
	insertArtist(t, s, 2, "approved")
	if _, err := s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	insertArtist(t, s, 3, "followed")
	if _, err := s.db.Exec(`UPDATE artists SET remote_followed=1 WHERE id=3`); err != nil {
		t.Fatal(err)
	}
	insertArtist(t, s, 4, "blocked")
	if _, err := s.db.Exec(`UPDATE artists SET review_status='blocked',remote_followed=1 WHERE id=4`); err != nil {
		t.Fatal(err)
	}
	seeds, err := s.RecommendationSeeds(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 || seeds[0].ID != 2 || seeds[1].ID != 3 {
		t.Fatalf("seeds=%+v", seeds)
	}
	_, _, j := runningRecommendation(t, s)
	art := domain.Artwork{ID: 9100, ArtistID: 1, Kind: "illust", PageCount: 0}
	if err = s.CommitSeedSamples(ctx, j, 1, []domain.Artwork{art}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pending seed commit error=%v", err)
	}
	art.ArtistID = 4
	if err = s.CommitSeedSamples(ctx, j, 4, []domain.Artwork{art}); err == nil {
		t.Fatal("blocked seed accepted")
	}
	art.ArtistID = 3
	if err = s.CommitSeedSamples(ctx, j, 3, []domain.Artwork{art}); err != nil {
		t.Fatalf("remote followed seed rejected: %v", err)
	}
}

func TestRecommendationSeedsPrioritizeRecentFollowsThenCreationButReturnIDOrder(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	for _, id := range []int64{5, 10, 20, 30, 40} {
		insertArtist(t, s, id, "seed")
		if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved',collection_enabled=1,remote_followed=1,created_at=? WHERE id=?`, id*10, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts(id,name,token_file,created_at,updated_at) VALUES('seed-account','seed','unused',1,1)`); err != nil {
		t.Fatal(err)
	}
	for _, relation := range []struct {
		artistID   int64
		followedAt int64
		followed   int
	}{{5, 999, 0}, {10, 200, 1}, {30, 300, 1}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,followed_at) VALUES('seed-account',?,?,?)`, relation.artistID, relation.followed, relation.followedAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET created_at=CASE id WHEN 20 THEN 2000 WHEN 40 THEN 1000 ELSE created_at END`); err != nil {
		t.Fatal(err)
	}
	seeds, err := s.RecommendationSeeds(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 3 || seeds[0].ID != 10 || seeds[1].ID != 20 || seeds[2].ID != 30 {
		t.Fatalf("recent subset in processing order = %+v, want IDs [10 20 30]", seeds)
	}
}

func TestCommitSeedSamplesAllowsOnlyCurrentPendingCandidatesWithoutCursorAdvance(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, job := runningRecommendation(t, s)
	candidates := map[int64]domain.Artist{
		71: {ID: 71, Name: "blocked after recall"},
		72: {ID: 72, Name: "rejected after recall"},
		73: {ID: 73, Name: "eligible pending"},
	}
	if err := s.CommitRecommendationRecall(ctx, job, runID, 77, candidates, nil, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='blocked' WHERE id=71`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='rejected' WHERE id=72`); err != nil {
		t.Fatal(err)
	}
	valid := domain.Artwork{ID: 7301, ArtistID: 73, Title: "preview", Kind: "illust", PageCount: 1, Rating: "safe", AIStatus: "non_ai", MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/img-original/7301_p0.jpg", State: "indexed"}}}
	wrongAuthor := valid
	wrongAuthor.ID = 7310
	wrongAuthor.ArtistID = 71
	if err := s.CommitSeedSamples(ctx, job, 73, []domain.Artwork{wrongAuthor}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("cross-artist sample error=%v, want ErrInvalid", err)
	}
	for _, id := range []int64{71, 72} {
		art := valid
		art.ID = id * 100
		art.ArtistID = id
		if err := s.CommitSeedSamples(ctx, job, id, []domain.Artwork{art}); !errors.Is(err, domain.ErrConflict) {
			t.Errorf("blocked/rejected candidate %d error=%v, want ErrConflict", id, err)
		}
	}
	if err := s.CommitSeedSamples(ctx, job, 73, []domain.Artwork{valid}); err != nil {
		t.Fatalf("current pending candidate preview rejected: %v", err)
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SeedCursor != 77 || run.CandidateCursor != 0 {
		t.Fatalf("sample commit advanced cursor: seed=%d candidate=%d", run.SeedCursor, run.CandidateCursor)
	}
	if err := s.CompleteRecommendation(ctx, job, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE recommendation_runs SET finished_at=? WHERE id=?`, time.Now().Add(-2*time.Hour).UnixMilli(), runID); err != nil {
		t.Fatal(err)
	}
	_, _, nextRunJob := runningRecommendation(t, s)
	if err := s.CommitSeedSamples(ctx, nextRunJob, 73, []domain.Artwork{valid}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-run candidate preview error=%v, want ErrConflict", err)
	}
}

func TestRecommendationCommitRequiresLiveLease(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, j := runningRecommendation(t, s)
	if _, err := s.db.Exec(`UPDATE jobs SET lease_until=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), j.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ReserveRecommendationRequest(ctx, j, runID, 2)
	if ok || !errors.Is(err, domain.ErrLease) {
		t.Fatalf("reservation=%v,%v", ok, err)
	}
}

func TestRecommendationCapPrecedesMetadataAndMergesExistingSources(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, j := runningRecommendation(t, s)
	first := domain.Artist{ID: 101, Name: "first"}
	second := domain.Artist{ID: 102, Name: "over cap"}
	if err := s.CommitRecommendationRecall(ctx, j, runID, 50, map[int64]domain.Artist{101: first, 102: second}, map[int64][]string{101: {"seed-a"}, 102: {"seed-b"}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitRecommendationRecall(ctx, j, runID, 20, map[int64]domain.Artist{101: first, 103: {ID: 103, Name: "also over cap"}}, map[int64][]string{101: {"seed-c"}, 103: {"seed-d"}}, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists WHERE id IN (101,102,103)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("artist metadata rows=%d, want only admitted candidate", count)
	}
	var sources int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_sources WHERE artist_id=101 AND source_type='recommend'`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != 2 {
		t.Fatalf("merged source rows=%d, want 2", sources)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_sources WHERE artist_id IN (102,103)`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != 0 {
		t.Fatalf("over-cap source rows=%d, want 0", sources)
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SeedCursor != 50 {
		t.Fatalf("seed cursor moved backwards: %d", run.SeedCursor)
	}
	items, err := s.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Sources) != 2 || items[0].Sources[0] != "seed-a" || items[0].Sources[1] != "seed-c" {
		t.Fatalf("candidate sources=%+v", items)
	}
}

func TestRecommendationRequestAndCandidateSampleLimits(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, j := runningRecommendation(t, s)
	for _, max := range []int{0, 301} {
		if _, err := s.ReserveRecommendationRequest(ctx, j, runID, max); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("reservation max=%d error=%v", max, err)
		}
	}
	result := domain.Recommendation{ArtistID: 1}
	if err := s.CommitRecommendationSamples(ctx, j, runID, 1, 1, make([]domain.Artwork, 5), result); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("five candidate samples error=%v", err)
	}
}

func TestRecommendationCandidateContentRatingUsesOnlySamples(t *testing.T) {
	s := openRecommendationStore(t)
	ctx, runID, job := runningRecommendation(t, s)
	ids := []int64{201, 202, 203, 204, 205, 206, 207, 208, 209, 210}
	candidates := map[int64]domain.Artist{}
	for _, id := range ids {
		candidates[id] = domain.Artist{ID: id, Name: "candidate"}
	}
	if err := s.CommitRecommendationRecall(ctx, job, runID, 0, candidates, nil, 60); err != nil {
		t.Fatal(err)
	}
	// Persisted sample IDs are normally JSON strings. Cover classification and
	// malformed/duplicate/overflow values directly in this fixture.
	samples := map[int64][]string{
		201: {"301"},
		202: {"302", "303"},
		203: {"304", "305"},
		204: {"306"},
		205: {"399"},
		206: {"310junk"},
		207: {"311"},
		209: {"313", "313"},
		210: {"314", "315", "316", "317", "318"},
	}
	artworks := []domain.Artwork{
		{ID: 301, ArtistID: 201, Rating: "r18", AIStatus: "non_ai"},
		{ID: 302, ArtistID: 202, Rating: "safe", AIStatus: "non_ai", Tags: []string{"common", "a", "b", "c", "d", "e", "f", "g", "h"}},
		{ID: 303, ArtistID: 202, Rating: "r18g", AIStatus: "ai", Tags: []string{"common", "h"}},
		{ID: 304, ArtistID: 203, Rating: "safe", AIStatus: "non_ai"},
		{ID: 305, ArtistID: 203, Rating: "safe", AIStatus: "non_ai"},
		{ID: 306, ArtistID: 204, Rating: "unknown", AIStatus: "unknown"},
		{ID: 307, ArtistID: 205, Rating: "safe", AIStatus: "non_ai"}, // unrelated to sample 399
		{ID: 310, ArtistID: 206, Rating: "safe", AIStatus: "non_ai"},
		{ID: 311, ArtistID: 208, Rating: "safe", AIStatus: "non_ai"}, // another artist's work
		{ID: 312, ArtistID: 208, Rating: "safe", AIStatus: "non_ai"},
		{ID: 313, ArtistID: 209, Rating: "safe", AIStatus: "non_ai"},
		{ID: 314, ArtistID: 210, Rating: "safe", AIStatus: "non_ai"},
		{ID: 315, ArtistID: 210, Rating: "safe", AIStatus: "non_ai"},
		{ID: 316, ArtistID: 210, Rating: "safe", AIStatus: "non_ai"},
		{ID: 317, ArtistID: 210, Rating: "safe", AIStatus: "non_ai"},
		{ID: 318, ArtistID: 210, Rating: "safe", AIStatus: "non_ai"},
	}
	for _, art := range artworks {
		art.Kind = "illust"
		art.PageCount = 0
		art.MetadataState = "complete"
		if err := s.UpsertArtwork(ctx, art); err != nil {
			t.Fatal(err)
		}
	}
	for artistID, sampleIDs := range samples {
		encoded, _ := json.Marshal(sampleIDs)
		if _, err := s.db.ExecContext(ctx, `UPDATE recommendations SET sample_illust_ids_json=? WHERE run_id=? AND artist_id=?`, string(encoded), runID, artistID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{201: "adult", 202: "adult", 203: "safe", 204: "unknown", 205: "unknown", 206: "unknown", 207: "unknown", 208: "unknown", 209: "safe", 210: "unknown"}
	wantAI := map[int64]string{201: "non_ai", 202: "ai", 203: "non_ai", 204: "unknown", 205: "unknown", 206: "unknown", 207: "unknown", 208: "unknown", 209: "non_ai", 210: "unknown"}
	for _, item := range got {
		if item.ArtistID == 202 && !reflect.DeepEqual(item.Tags, []string{"common", "h", "a", "b", "c", "d", "e", "f"}) {
			t.Errorf("sample tags=%v, want top eight ordered by frequency then name", item.Tags)
		}
		if item.ArtistID == 207 && len(item.Tags) != 0 {
			t.Errorf("another artist's sample supplied tags: %v", item.Tags)
		}
		if item.ContentRating != want[item.ArtistID] {
			t.Errorf("artist %d rating=%q want %q", item.ArtistID, item.ContentRating, want[item.ArtistID])
		}
		if item.AIStatus != wantAI[item.ArtistID] {
			t.Errorf("artist %d ai_status=%q want %q", item.ArtistID, item.AIStatus, wantAI[item.ArtistID])
		}
	}
}

func TestParseRecommendationSampleIDsRejectsMalformedAndNormalizes(t *testing.T) {
	for _, raw := range []string{`["312"`, `null`, `{"id":"312"}`} {
		ids, invalid, _ := parseRecommendationSampleIDs(raw)
		if !invalid || len(ids) != 0 {
			t.Errorf("parse(%q)=%v invalid=%v, want invalid empty result", raw, ids, invalid)
		}
	}
	ids, invalid, unknown := parseRecommendationSampleIDs(`["301","301","0","301junk"]`)
	if !invalid || unknown != 2 || len(ids) != 1 || ids[0] != "301" {
		t.Fatalf("normalized IDs=%v invalid=%v unknown=%d", ids, invalid, unknown)
	}
	ids, invalid, unknown = parseRecommendationSampleIDs(`[302]`)
	if invalid || unknown != 0 || len(ids) != 1 || ids[0] != "302" {
		t.Fatalf("numeric ID compatibility=%v invalid=%v unknown=%d", ids, invalid, unknown)
	}
}

func TestLargerRecommendationBudgetDoesNotReuseSmallCachedRun(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	old, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"candidate_artists":12}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRecommendation(ctx, job, old); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"candidate_artists":48}`))
	if err != nil || current == old {
		t.Fatalf("larger request must start a new run: %s %v", current, err)
	}
}

func TestRecommendationOnlyReusesActiveJob(t *testing.T) {
	s := openRecommendationStore(t)
	ctx := context.Background()
	budget := json.RawMessage(`{}`)
	first, job, err := s.StartRecommendation(ctx, budget)
	if err != nil {
		t.Fatal(err)
	}
	again, againJob, err := s.StartRecommendation(ctx, budget)
	if err != nil || again != first || againJob != job {
		t.Fatalf("active task not deduplicated: %s %s %v", again, againJob, err)
	}
}
