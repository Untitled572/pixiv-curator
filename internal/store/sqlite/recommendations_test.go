package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
	if _, _, err = s.StartRecommendation(ctx, json.RawMessage(`{}`)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("recent completed run error=%v, want ErrConflict", err)
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
