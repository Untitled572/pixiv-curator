package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type fakeRecommendationClient struct {
	mu                  sync.Mutex
	artistCalls         map[int64]int
	relatedArtistCalls  map[int64]int
	relatedArtworkCalls map[int64]int
	relatedArtists      func(context.Context, int64) ([]pixiv.RawArtist, error)
	relatedArtworks     func(context.Context, int64) ([]pixiv.RawArtwork, error)
	artistArtworks      func(context.Context, int64, string) (pixiv.ArtworkPage, error)
}

type fakeRecommendationPreviewClient struct {
	*fakeRecommendationClient
	previews func(context.Context, int64) ([]pixiv.UserPreview, error)
}

func (f *fakeRecommendationPreviewClient) RelatedArtistPreviews(ctx context.Context, id int64) ([]pixiv.UserPreview, error) {
	f.mu.Lock()
	f.relatedArtistCalls[id]++
	f.mu.Unlock()
	if f.previews == nil {
		return nil, nil
	}
	return f.previews(ctx, id)
}

func newFakeRecommendationClient() *fakeRecommendationClient {
	return &fakeRecommendationClient{artistCalls: map[int64]int{}, relatedArtistCalls: map[int64]int{}, relatedArtworkCalls: map[int64]int{}}
}
func (f *fakeRecommendationClient) RelatedArtists(ctx context.Context, id int64) ([]pixiv.RawArtist, error) {
	f.mu.Lock()
	f.relatedArtistCalls[id]++
	f.mu.Unlock()
	if f.relatedArtists == nil {
		return nil, nil
	}
	return f.relatedArtists(ctx, id)
}
func (f *fakeRecommendationClient) RelatedArtworks(ctx context.Context, id int64) ([]pixiv.RawArtwork, error) {
	f.mu.Lock()
	f.relatedArtworkCalls[id]++
	f.mu.Unlock()
	if f.relatedArtworks == nil {
		return nil, nil
	}
	return f.relatedArtworks(ctx, id)
}
func (f *fakeRecommendationClient) ArtistArtworks(ctx context.Context, id int64, cursor string) (pixiv.ArtworkPage, error) {
	f.mu.Lock()
	f.artistCalls[id]++
	f.mu.Unlock()
	if f.artistArtworks == nil {
		return pixiv.ArtworkPage{}, nil
	}
	return f.artistArtworks(ctx, id, cursor)
}
func (f *fakeRecommendationClient) countAll() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, n := range f.artistCalls {
		total += n
	}
	for _, n := range f.relatedArtistCalls {
		total += n
	}
	for _, n := range f.relatedArtworkCalls {
		total += n
	}
	return total
}
func (f *fakeRecommendationClient) countArtist(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.artistCalls[id]
}

func openRecommendationAppStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "recommendations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func addApprovedRecommendationSeed(t *testing.T, s *sqlite.Store, id int64) {
	t.Helper()
	ctx := context.Background()
	c := NewCatalog(s)
	if err := c.ImportArtist(ctx, id); err != nil {
		t.Fatal(err)
	}
	jobID, err := c.DecideArtist(ctx, id, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(ctx, jobID); err != nil {
		t.Fatal(err)
	}
}
func createRecommendationJob(t *testing.T, s *sqlite.Store, budget recommend.Budget) (string, domain.Job) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}
	runID, _, err := s.StartRecommendation(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != "recommend_artists" {
		t.Fatalf("claimed job kind=%q", j.Kind)
	}
	return runID, j
}
func recArtwork(id, artistID int64, tags ...string) pixiv.RawArtwork {
	r := rawArtwork(id, artistID)
	for _, tag := range tags {
		r.Tags = append(r.Tags, struct {
			Name string `json:"name"`
		}{Name: tag})
	}
	return r
}
func storedArtwork(id, artistID int64, tags ...string) domain.Artwork {
	return domain.Artwork{ID: id, ArtistID: artistID, Kind: "illust", Rating: "safe", AIStatus: "non_ai", PageCount: 0, MetadataState: "complete", Tags: tags}
}

func TestRecommendationDefaultsStayBoundedAndOutputNeedsApproval(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	const seedBase int64 = 10000
	for i := int64(0); i < 8; i++ {
		addApprovedRecommendationSeed(t, s, seedBase+i)
	}
	if err := s.UpsertArtwork(ctx, storedArtwork(999999, seedBase, "avoid-tag")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtworkPreference(ctx, 999999, "dislike"); err != nil {
		t.Fatal(err)
	}
	fake := newFakeRecommendationClient()
	fake.artistArtworks = func(_ context.Context, id int64, _ string) (pixiv.ArtworkPage, error) {
		count := 9
		if id >= 30000 {
			count = 5
		}
		items := make([]pixiv.RawArtwork, count)
		for i := range items {
			items[i] = recArtwork(id*100+int64(i), id, "positive-tag")
		}
		return pixiv.ArtworkPage{Artworks: items}, nil
	}
	fake.relatedArtists = func(_ context.Context, id int64) ([]pixiv.RawArtist, error) {
		if id == seedBase+1 {
			return nil, &pixiv.Error{Code: "UPSTREAM_ERROR", Status: 500}
		}
		if id != seedBase {
			return nil, nil
		}
		out := make([]pixiv.RawArtist, 60)
		for i := range out {
			out[i] = rawArtist(30000+int64(i), fmt.Sprintf("candidate-%d", i))
		}
		return out, nil
	}
	fake.relatedArtworks = func(_ context.Context, id int64) ([]pixiv.RawArtwork, error) {
		if id != seedBase*100 {
			return nil, nil
		}
		return []pixiv.RawArtwork{{ID: 880001, User: rawArtist(30000, "candidate-0")}}, nil
	}
	service := NewRecommender(s, fake)
	runID, job := createRecommendationJob(t, s, recommend.DefaultBudget())
	if err := service.Handler(ctx, job); err != nil {
		t.Fatal(err)
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("run state=%q", run.State)
	}
	var budget recommend.Budget
	if err = json.Unmarshal(run.Budget, &budget); err != nil {
		t.Fatal(err)
	}
	if budget != recommend.DefaultBudget() {
		t.Fatalf("stored budget=%+v", budget)
	}
	if run.RequestCount > 60 || fake.countAll() > 60 {
		t.Fatalf("request budget exceeded: reserved=%d client calls=%d", run.RequestCount, fake.countAll())
	}
	if run.RequestCount != fake.countAll() {
		t.Fatalf("reserved requests=%d fake upstream calls=%d", run.RequestCount, fake.countAll())
	}
	seeds, err := s.RecommendationSeeds(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 8 {
		t.Fatalf("eligible seeds=%d", len(seeds))
	}
	for _, seed := range seeds {
		samples, err := s.RecommendationSamples(ctx, seed.ID, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != 4 {
			t.Fatalf("seed %d sample count=%d, want 4", seed.ID, len(samples))
		}
	}
	candidates, err := s.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 12 {
		t.Fatalf("candidate count=%d, want 12", len(candidates))
	}
	for _, candidate := range candidates {
		samples, err := s.RecommendationSamples(ctx, candidate.ArtistID, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != 2 {
			t.Fatalf("candidate %d sample count=%d, want max 2", candidate.ArtistID, len(samples))
		}
		if candidate.Artist.ReviewStatus != "pending" || candidate.Artist.CollectionEnabled {
			t.Fatalf("candidate auto-approved: %+v", candidate.Artist)
		}
	}
	first := candidates[0]
	if first.ArtistID != 30000 || len(first.Sources) != 2 || first.Sources[0] != recommend.SourceRelatedArtist || first.Sources[1] != recommend.SourceRelatedArtwork {
		t.Fatalf("merged sources after optional 5xx: %+v", first)
	}
	if fake.countAll() > 60 {
		t.Fatalf("fake client calls=%d", fake.countAll())
	}
	profileRaw, err := s.RecommendationProfile(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var profile recommend.Profile
	if err = json.Unmarshal(profileRaw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.NegativeTagWeights["avoid-tag"] <= 0 {
		t.Fatalf("explicitly disliked tag missing from frozen profile: %+v", profile.NegativeTagWeights)
	}
	firstSampleID, parseErr := strconv.ParseInt(first.SampleIDs[0], 10, 64)
	if parseErr != nil {
		t.Fatalf("candidate sample ID %q: %v", first.SampleIDs[0], parseErr)
	}
	if _, err = s.Select(ctx, domain.SelectionRequest{SelectionRules: domain.DefaultRules(), ArtistID: first.ArtistID}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("pending recommendation sample entered image selection: %v", err)
	}
	if _, err = s.EligiblePage(ctx, firstSampleID, 0, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pending recommendation sample became output eligible: %v", err)
	}
	jobID, err := NewCatalog(s).DecideArtist(ctx, first.ArtistID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Cancel(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EligiblePage(ctx, firstSampleID, 0, false); err != nil {
		t.Fatalf("explicitly approved candidate sample is not eligible: %v", err)
	}
	if _, err = s.Select(ctx, domain.SelectionRequest{SelectionRules: domain.DefaultRules(), ArtistID: first.ArtistID}); err != nil {
		t.Fatalf("explicitly approved candidate cannot be selected: %v", err)
	}
	jobs, err := s.Jobs(ctx, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	var succeeded bool
	for _, item := range jobs {
		if item.ID == job.ID && item.State == "succeeded" {
			succeeded = true
		}
	}
	if !succeeded {
		t.Fatalf("recommendation job state not succeeded: %+v", jobs)
	}
}

func TestRecommendationRequestBudgetPreventsExtraClientCalls(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	addApprovedRecommendationSeed(t, s, 40001)
	budget := recommend.Budget{SeedArtists: 30, SeedSamplesPerArtist: 8, CandidateArtists: 60, CandidateSamplesPerArtist: 4, TotalRequests: 1}
	fake := newFakeRecommendationClient()
	fake.artistArtworks = func(_ context.Context, id int64, _ string) (pixiv.ArtworkPage, error) {
		return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{recArtwork(id*100, id, "tag")}}, nil
	}
	service := NewRecommender(s, fake)
	runID, job := createRecommendationJob(t, s, budget)
	if err := service.Handler(ctx, job); err != nil {
		t.Fatal(err)
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.RequestCount != 1 || fake.countAll() != 1 {
		t.Fatalf("reserved/calls=%d/%d, want one", run.RequestCount, fake.countAll())
	}
}

func TestRecommendationResumeAfterSeedFailureDoesNotRefetchCommittedSeed(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	seed1, seed2 := int64(41001), int64(41002)
	addApprovedRecommendationSeed(t, s, seed1)
	addApprovedRecommendationSeed(t, s, seed2)
	fake := newFakeRecommendationClient()
	secondFails := true
	fake.artistArtworks = func(_ context.Context, id int64, _ string) (pixiv.ArtworkPage, error) {
		if id == seed2 && secondFails {
			secondFails = false
			return pixiv.ArtworkPage{}, &pixiv.Error{Code: "AUTH_REQUIRED", Status: 401}
		}
		return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{recArtwork(id*100, id, "seed-tag")}}, nil
	}
	service := NewRecommender(s, fake)
	runID, job := createRecommendationJob(t, s, recommend.DefaultBudget())
	if err := service.Handler(ctx, job); err == nil {
		t.Fatal("expected second seed auth failure")
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SeedCursor != seed1 {
		t.Fatalf("committed seed cursor=%d, want %d", run.SeedCursor, seed1)
	}
	if fake.countArtist(seed1) != 1 {
		t.Fatalf("seed one requests before retry=%d", fake.countArtist(seed1))
	}
	if err := service.Handler(ctx, job); err != nil {
		t.Fatalf("resume handler: %v", err)
	}
	if fake.countArtist(seed1) != 1 || fake.countArtist(seed2) != 2 {
		t.Fatalf("seed calls after resume first=%d second=%d", fake.countArtist(seed1), fake.countArtist(seed2))
	}
	run, err = s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SeedCursor != seed2 || run.State != "succeeded" {
		t.Fatalf("resumed run=%+v", run)
	}
}

func TestRecommendationResumeKeepsFrozenProfileAndCandidateCursor(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	seed := int64(42001)
	addApprovedRecommendationSeed(t, s, seed)
	if err := s.UpsertArtwork(ctx, storedArtwork(920001, seed, "positive-tag")); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, storedArtwork(920002, seed, "avoid-tag")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArtworkPreference(ctx, 920002, "dislike"); err != nil {
		t.Fatal(err)
	}
	fake := newFakeRecommendationClient()
	candidateFails := true
	fake.relatedArtists = func(context.Context, int64) ([]pixiv.RawArtist, error) {
		return []pixiv.RawArtist{rawArtist(42010, "candidate-1"), rawArtist(42020, "candidate-2")}, nil
	}
	fake.artistArtworks = func(_ context.Context, id int64, _ string) (pixiv.ArtworkPage, error) {
		if id == 42020 && candidateFails {
			candidateFails = false
			return pixiv.ArtworkPage{}, &pixiv.Error{Code: "AUTH_REQUIRED", Status: 401}
		}
		return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{recArtwork(id*100, id, "positive-tag")}}, nil
	}
	service := NewRecommender(s, fake)
	runID, job := createRecommendationJob(t, s, recommend.DefaultBudget())
	if err := service.Handler(ctx, job); err == nil {
		t.Fatal("expected candidate sample auth failure")
	}
	run, err := s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CandidateCursor != 42010 {
		t.Fatalf("candidate cursor=%d, want first committed candidate", run.CandidateCursor)
	}
	frozenBefore, err := s.RecommendationProfile(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var before recommend.Profile
	if err = json.Unmarshal(frozenBefore, &before); err != nil {
		t.Fatal(err)
	}
	if before.NegativeTagWeights["avoid-tag"] <= 0 {
		t.Fatalf("profile did not capture negative tag: %+v", before)
	}
	if err := s.SetArtworkPreference(ctx, 920002, "neutral"); err != nil {
		t.Fatal(err)
	}
	if err := service.Handler(ctx, job); err != nil {
		t.Fatalf("resume candidate scoring: %v", err)
	}
	frozenAfter, err := s.RecommendationProfile(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var after recommend.Profile
	if err = json.Unmarshal(frozenAfter, &after); err != nil {
		t.Fatal(err)
	}
	if after.NegativeTagWeights["avoid-tag"] <= 0 || string(frozenAfter) != string(frozenBefore) {
		t.Fatalf("profile changed on resume: before=%s after=%s", frozenBefore, frozenAfter)
	}
	if fake.countArtist(42010) != 1 || fake.countArtist(42020) != 2 {
		t.Fatalf("candidate fetch counts first=%d second=%d", fake.countArtist(42010), fake.countArtist(42020))
	}
	run, err = s.RecommendationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CandidateCursor != 42020 || run.State != "succeeded" {
		t.Fatalf("completed resumed run=%+v", run)
	}
}

func TestRecommendationStartIsIdempotentAndEmptySeedsComplete(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	service := NewRecommender(s, newFakeRecommendationClient())
	run1, job1, err := service.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run2, job2, err := service.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run1 != run2 || job1 != job2 {
		t.Fatalf("duplicate starts differ: %s/%s %s/%s", run1, job1, run2, job2)
	}
	j, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != job1 {
		t.Fatalf("claimed job=%s want %s", j.ID, job1)
	}
	if err = service.Handler(ctx, j); err != nil {
		t.Fatal(err)
	}
	run, err := s.RecommendationRun(ctx, run1)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "succeeded" {
		t.Fatalf("empty-seed run state=%q", run.State)
	}
	if jobs, err := s.Jobs(ctx, "", 10); err != nil {
		t.Fatal(err)
	} else {
		for _, item := range jobs {
			if item.ID == job1 && item.State != "succeeded" {
				t.Fatalf("empty-seed job state=%q", item.State)
			}
		}
	}
}

func TestRecommendationUsesRelatedPreviewSamplesWithoutCandidateArtworkFetch(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	const seedID int64 = 49000
	addApprovedRecommendationSeed(t, s, seedID)
	if err := s.UpsertArtwork(ctx, storedArtwork(49001, seedID, "seed-tag")); err != nil {
		t.Fatal(err)
	}
	base := newFakeRecommendationClient()
	base.relatedArtworks = func(context.Context, int64) ([]pixiv.RawArtwork, error) { return nil, nil }
	client := &fakeRecommendationPreviewClient{
		fakeRecommendationClient: base,
		previews: func(_ context.Context, id int64) ([]pixiv.UserPreview, error) {
			if id != seedID {
				t.Errorf("related seed=%d, want %d", id, seedID)
			}
			return []pixiv.UserPreview{
				{User: rawArtist(50001, "with two samples"), Illusts: []pixiv.RawArtwork{recArtwork(500011, 50001, "match"), recArtwork(500012, 50001, "second")}},
				{User: rawArtist(50002, "identity checked"), Illusts: []pixiv.RawArtwork{recArtwork(500021, 50002, "match"), recArtwork(509999, 99999, "must-not-persist")}},
			}, nil
		},
	}
	budget := recommend.Budget{SeedArtists: 1, SeedSamplesPerArtist: 1, CandidateArtists: 2, CandidateSamplesPerArtist: 2, TotalRequests: 10}
	runID, job := createRecommendationJob(t, s, budget)
	service := NewRecommender(s, client)
	if err := service.Handler(ctx, job); err != nil {
		t.Fatal(err)
	}
	if base.countArtist(50001) != 0 || base.countArtist(50002) != 0 {
		t.Fatalf("candidate artwork requests = %d/%d, want no per-candidate requests", base.countArtist(50001), base.countArtist(50002))
	}
	if base.countAll() != 2 {
		t.Fatalf("related endpoint requests = %d, want one user-preview and one related-artwork request", base.countAll())
	}
	candidates, err := s.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count=%d, want 2", len(candidates))
	}
	byID := make(map[int64]domain.Recommendation, len(candidates))
	for _, candidate := range candidates {
		byID[candidate.ArtistID] = candidate
		if candidate.Artist.ReviewStatus != "pending" || candidate.Artist.CollectionEnabled {
			t.Fatalf("candidate was automatically approved: %+v", candidate.Artist)
		}
	}
	if got := byID[50001].SampleIDs; !reflect.DeepEqual(got, []string{"500011", "500012"}) {
		t.Fatalf("first candidate sample IDs=%v", got)
	}
	if got := byID[50002].SampleIDs; !reflect.DeepEqual(got, []string{"500021"}) {
		t.Fatalf("identity-mismatched preview was retained: sample IDs=%v", got)
	}
	for id, want := range map[int64]int{50001: 2, 50002: 1} {
		samples, err := s.RecommendationSamples(ctx, id, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(samples) != want {
			t.Errorf("candidate %d persisted samples=%d, want %d", id, len(samples), want)
		}
		for _, sample := range samples {
			if sample.ArtistID != id {
				t.Errorf("candidate %d received sample from artist %d", id, sample.ArtistID)
			}
		}
	}
}

func TestRecommendationPreviewErrorsKeepCancelAnd429Classification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		check func(*testing.T, error)
	}{
		{name: "cancel", err: context.Canceled, check: func(t *testing.T, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error=%v", err)
			}
		}},
		{name: "rate limit", err: &pixiv.Error{Code: "RATE_LIMITED", Status: 429, RetryAfter: 17 * time.Second}, check: func(t *testing.T, err error) {
			var jobErr *jobs.JobError
			if !errors.As(err, &jobErr) || jobErr.Class != jobs.Retry || jobErr.Code != "RATE_LIMITED" || jobErr.RetryAfter != 17*time.Second {
				t.Fatalf("rate-limit classification=%+v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openRecommendationAppStore(t)
			const seedID int64 = 49500
			addApprovedRecommendationSeed(t, s, seedID)
			if err := s.UpsertArtwork(ctx, storedArtwork(49501, seedID, "seed")); err != nil {
				t.Fatal(err)
			}
			base := newFakeRecommendationClient()
			client := &fakeRecommendationPreviewClient{fakeRecommendationClient: base, previews: func(context.Context, int64) ([]pixiv.UserPreview, error) {
				return nil, tc.err
			}}
			runID, job := createRecommendationJob(t, s, recommend.Budget{SeedArtists: 1, SeedSamplesPerArtist: 1, CandidateArtists: 2, CandidateSamplesPerArtist: 2, TotalRequests: 3})
			if err := NewRecommender(s, client).Handler(ctx, job); err == nil {
				t.Fatal("expected preview request error")
			} else {
				tc.check(t, err)
			}
			run, err := s.RecommendationRun(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if run.SeedCursor != 0 {
				t.Fatalf("failed preview advanced seed cursor to %d", run.SeedCursor)
			}
		})
	}
}

func TestCandidatesFilteredCombinesSampleClassifications(t *testing.T) {
	s := openRecommendationAppStore(t)
	ctx := context.Background()
	runID, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"max_requests":2}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[int64]domain.Artist{
		801: {ID: 801, Name: "adult unknown AI"},
		802: {ID: 802, Name: "safe AI"},
		803: {ID: 803, Name: "unknown"},
	}
	if err = s.CommitRecommendationRecall(ctx, job, runID, 0, candidates, nil, 60); err != nil {
		t.Fatal(err)
	}
	works := []domain.Artwork{
		{ID: 811, ArtistID: 801, Kind: "illust", Rating: "r18", AIStatus: "non_ai"},
		{ID: 812, ArtistID: 801, Kind: "illust", Rating: "safe", AIStatus: "unknown"},
		{ID: 821, ArtistID: 802, Kind: "illust", Rating: "safe", AIStatus: "ai"},
	}
	for _, work := range works {
		if err = s.UpsertArtwork(ctx, work); err != nil {
			t.Fatal(err)
		}
	}
	for artistID, sampleIDs := range map[int64][]string{801: {"811", "812"}, 802: {"821"}, 803: {"899"}} {
		result := domain.Recommendation{ArtistID: artistID, SampleIDs: sampleIDs}
		if err = s.CommitRecommendationSamples(ctx, job, runID, artistID, artistID+1, nil, result); err != nil {
			t.Fatal(err)
		}
	}
	service := NewRecommender(s, newFakeRecommendationClient())
	_, all, err := service.CandidatesFiltered(ctx, runID, "all", "all")
	if err != nil || len(all) != 3 {
		t.Fatalf("all candidates=%d err=%v", len(all), err)
	}
	_, adultUnknownAI, err := service.CandidatesFiltered(ctx, runID, "adult", "unknown")
	if err != nil || len(adultUnknownAI) != 1 || adultUnknownAI[0].ArtistID != 801 {
		t.Fatalf("adult + unknown AI candidates=%+v err=%v", adultUnknownAI, err)
	}
	_, safeAI, err := service.CandidatesFiltered(ctx, runID, "safe", "ai")
	if err != nil || len(safeAI) != 1 || safeAI[0].ArtistID != 802 {
		t.Fatalf("safe + AI candidates=%+v err=%v", safeAI, err)
	}
}
