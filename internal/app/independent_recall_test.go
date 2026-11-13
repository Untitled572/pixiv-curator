package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type independentPipelineClient struct {
	*fakeRecommendationClient
	mu      sync.Mutex
	queries []struct {
		mode, date string
		offset     int
	}
	artworkIDs []int64
	rankOwner  map[int64]int64
}

func newIndependentPipelineClient() *independentPipelineClient {
	return &independentPipelineClient{fakeRecommendationClient: newFakeRecommendationClient(), rankOwner: map[int64]int64{}}
}

func (c *independentPipelineClient) ExplorationArtworks(_ context.Context, mode, date string, offset int) ([]pixiv.RawArtwork, error) {
	c.mu.Lock()
	c.queries = append(c.queries, struct {
		mode, date string
		offset     int
	}{mode, date, offset})
	queryNumber := len(c.queries)
	c.mu.Unlock()
	works := make([]pixiv.RawArtwork, 20)
	for i := range works {
		id, artistID := int64(100000+i), int64(20000+i)
		if queryNumber > 1 {
			id += int64(queryNumber * 100)
			artistID += int64(queryNumber * 100)
		}
		c.mu.Lock()
		c.rankOwner[id] = artistID
		c.mu.Unlock()
		works[i] = rawArtwork(id, artistID)
	}
	return works, nil
}

func (c *independentPipelineClient) Artwork(_ context.Context, illustID int64) (pixiv.RawArtwork, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.artworkIDs = append(c.artworkIDs, illustID)
	if owner, ok := c.rankOwner[illustID]; ok {
		return rawArtwork(illustID, owner), nil
	}
	if len(c.artworkIDs) == 1 {
		return rawArtwork(illustID, 20000), nil
	}
	return rawArtwork(illustID, 30000+int64(len(c.artworkIDs))), nil
}

func TestIndependentRecommendationPipelineFreezesRankingAndUniformSources(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "independent-pipeline.db")
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	client := newIndependentPipelineClient()
	service := NewRecommender(store, client)
	rawBudget := service.defaultRunBudget()
	var defaultFields map[string]any
	if err = json.Unmarshal(rawBudget, &defaultFields); err != nil {
		t.Fatal(err)
	}
	if defaultFields["independent_exploration"] != true {
		t.Fatalf("default budget did not enable independent sources: %s", rawBudget)
	}
	runID, _, err := store.StartRecommendation(ctx, rawBudget)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		CandidateArtists   int     `json:"candidate_artists"`
		AlgorithmArtists   int     `json:"algorithm_artists"`
		RankingArtists     int     `json:"ranking_artists"`
		UniformTarget      int     `json:"uniform_sample_target"`
		ExplorationArtists int     `json:"exploration_artists"`
		IndependentLimit   int     `json:"independent_candidate_limit"`
		Cursor             int     `json:"exploration_cursor"`
		UniformCursor      int     `json:"uniform_cursor"`
		RankingMax         int64   `json:"ranking_max_illust_id"`
		ProbeIDs           []int64 `json:"uniform_probe_ids"`
		Queries            []struct {
			Mode   string `json:"mode"`
			Date   string `json:"date"`
			Offset int    `json:"offset"`
			Seed   int64  `json:"shuffle_seed"`
		} `json:"exploration_queries"`
	}
	if err = json.Unmarshal(run.Budget, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.CandidateArtists != 30 || plan.AlgorithmArtists != 17 || plan.RankingArtists != 10 || plan.UniformTarget != 3 || plan.ExplorationArtists != 13 || plan.IndependentLimit != 26 || len(plan.Queries) != 4 {
		t.Fatalf("frozen cohort budget=%+v", plan)
	}
	wantDate := time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02")
	if plan.Queries[0].Mode != "day" || plan.Queries[0].Date != wantDate || plan.Queries[0].Offset != 0 {
		t.Fatalf("first query=%+v, want fixed day query on %s", plan.Queries[0], wantDate)
	}
	for _, query := range plan.Queries {
		if query.Seed == 0 || query.Offset < 0 || query.Offset > 270 || query.Offset%30 != 0 {
			t.Fatalf("query outside frozen bounds: %+v", query)
		}
	}
	if err = service.recallIndependent(ctx, job, run, 60, 80, map[int64]bool{}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	queryCalls := append([]struct {
		mode, date string
		offset     int
	}(nil), client.queries...)
	probeCalls := append([]int64(nil), client.artworkIDs...)
	client.mu.Unlock()
	if len(queryCalls) != 4 || len(probeCalls) != 3 {
		t.Fatalf("ranking requests=%d uniform probes=%d", len(queryCalls), len(probeCalls))
	}
	for i, query := range queryCalls {
		if query.mode != plan.Queries[i].Mode || query.date != plan.Queries[i].Date || query.offset != plan.Queries[i].Offset {
			t.Fatalf("request %d drifted from frozen query: got=%+v want=%+v", i, query, plan.Queries[i])
		}
	}
	run, err = store.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(run.Budget, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.RankingMax != 100019 || len(plan.ProbeIDs) != 18 || plan.Cursor != 4 || plan.UniformCursor != len(plan.ProbeIDs) {
		t.Fatalf("checkpointed exploration state=%+v", plan)
	}
	seenProbe := map[int64]bool{}
	for _, id := range plan.ProbeIDs {
		if id <= 0 || id > plan.RankingMax || seenProbe[id] {
			t.Fatalf("invalid/repeated frozen probe ID %d", id)
		}
		seenProbe[id] = true
	}
	for _, id := range probeCalls {
		if id <= 0 || id > plan.RankingMax {
			t.Fatalf("uniform probe %d exceeded ranking upper bound", id)
		}
	}
	var stableBudget json.RawMessage
	if stableBudget, err = json.Marshal(plan); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service = NewRecommender(store, client)
	run, err = store.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.recallIndependent(ctx, job, run, 60, 80, map[int64]bool{}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	if len(client.queries) != 4 || len(client.artworkIDs) != 3 {
		t.Fatalf("restart repeated a completed request: queries=%d probes=%d", len(client.queries), len(client.artworkIDs))
	}
	client.mu.Unlock()
	if !json.Valid(stableBudget) || !json.Valid(run.Budget) {
		t.Fatalf("invalid frozen budget after restart")
	}
	var beforePlan, afterPlan map[string]json.RawMessage
	_ = json.Unmarshal(stableBudget, &beforePlan)
	_ = json.Unmarshal(run.Budget, &afterPlan)
	for _, key := range []string{"exploration_queries", "shuffle_seed", "uniform_probe_ids", "ranking_max_illust_id"} {
		if string(beforePlan[key]) != string(afterPlan[key]) {
			t.Fatalf("frozen field %s changed after restart", key)
		}
	}

	// Fill the algorithm pool through the same SQLite admission API, then run
	// finalization to exercise the complete 17:10:3 source mix and display order.
	algorithmArtists := map[int64]domain.Artist{}
	algorithmSources := map[int64][]string{}
	algorithmSamples := map[int64][]domain.Artwork{}
	for i := int64(0); i < 17; i++ {
		artistID := int64(50000) + i
		artist, artistErr := pixiv.NormalizeArtist(rawArtist(artistID, fmt.Sprintf("algorithm-%d", i)))
		if artistErr != nil {
			t.Fatal(artistErr)
		}
		work, workErr := pixiv.NormalizeArtwork(rawArtwork(60000+i, artistID))
		if workErr != nil {
			t.Fatal(workErr)
		}
		algorithmArtists[artistID] = artist
		algorithmSources[artistID] = []string{"related_artist"}
		algorithmSamples[artistID] = []domain.Artwork{work}
	}
	if err = store.CommitRecommendationRecall(ctx, job, runID, run.SeedCursor, algorithmArtists, algorithmSources, 60); err != nil {
		t.Fatal(err)
	}
	if err = service.commitIndependentSamples(ctx, job, runID, algorithmSamples, "related_artist"); err != nil {
		t.Fatal(err)
	}
	if err = service.Handler(ctx, job); err != nil {
		t.Fatal(err)
	}
	_, first, err := service.Candidates(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := service.Candidates(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 30 || len(second) != len(first) {
		t.Fatalf("candidate result lengths=%d/%d", len(first), len(second))
	}
	pages := map[int64]bool{}
	for _, candidate := range first {
		if containsRecommendationSource(candidate.Sources, independentRecallSource) {
			if candidate.ArtistID < 20200 {
				t.Fatalf("bound-only current day artist admitted: %d", candidate.ArtistID)
			}
			pages[(candidate.ArtistID-20000)/100] = true
		}
	}
	if len(pages) < 2 {
		t.Fatalf("historical pages crowded out by the first page: %v", pages)
	}
	finalRun, err := store.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]int
	var finalBudget map[string]json.RawMessage
	if err = json.Unmarshal(finalRun.Budget, &finalBudget); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(finalBudget["actual_source_counts"], &actual); err != nil {
		t.Fatal(err)
	}
	if actual["algorithm"] != 17 || actual[independentRecallSource] != 10 || actual[independentUniformSource] != 3 {
		t.Fatalf("actual admitted source counts=%v", actual)
	}
	if run, err = store.RecommendationRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if run.RequestCount > 80 {
		t.Fatalf("reserved upstream requests=%d > 80", run.RequestCount)
	}
	for i := range first {
		if first[i].ArtistID != second[i].ArtistID {
			t.Fatalf("display ordering drift at %d: %d != %d", i, first[i].ArtistID, second[i].ArtistID)
		}
		var components struct {
			DisplayOrder int     `json:"display_order"`
			DisplayScore float64 `json:"display_score"`
		}
		if err = json.Unmarshal(first[i].Components, &components); err != nil {
			t.Fatal(err)
		}
		if components.DisplayOrder != i || components.DisplayScore < 0 || components.DisplayScore > 1 {
			t.Fatalf("candidate %d display data=%+v", i, components)
		}
	}
}
