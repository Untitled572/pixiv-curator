package recommend

import (
	"math"
	"reflect"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestDefaultBudget(t *testing.T) {
	want := Budget{
		SeedArtists: 8, SeedSamplesPerArtist: 4,
		CandidateArtists: 30, CandidateSamplesPerArtist: 3,
		TotalRequests: 80, RecallArtists: 60, ExplorationArtists: 6,
	}
	if got := DefaultBudget(); got != want {
		t.Fatalf("DefaultBudget() = %+v, want %+v", got, want)
	}
}

func TestBuildProfileBalancesArtistsAndExcludesIneligibleSeeds(t *testing.T) {
	seeds := []SeedArtist{
		{ArtistID: 2, ReviewStatus: "approved", Samples: []domain.Artwork{{ID: 20, ArtistID: 2, Tags: []string{"solo"}}}},
		{ArtistID: 1, ReviewStatus: "pending", RemoteFollowed: true, Samples: []domain.Artwork{{ID: 10, ArtistID: 1, Tags: []string{"a", "b", "c", "d", "a"}}}},
		{ArtistID: 3, ReviewStatus: "pending", Samples: []domain.Artwork{{ID: 30, ArtistID: 3, Tags: []string{"candidate-only"}}}},
		{ArtistID: 4, ReviewStatus: "blocked", RemoteFollowed: true, Samples: []domain.Artwork{{ID: 40, ArtistID: 4, Tags: []string{"blocked-only"}}}},
		{ArtistID: 5, ReviewStatus: "rejected", RemoteFollowed: true, Samples: []domain.Artwork{{ID: 50, ArtistID: 5, Tags: []string{"rejected-only"}}}},
	}
	profile := BuildProfile(seeds, map[string]float64{" Bad ": 3, "bad": 1, "": 5}, DefaultBudget())
	if profile.SeedArtistCount != 2 || profile.SeedSampleCount != 2 {
		t.Fatalf("eligible seed counts = %d artists/%d samples, want 2/2", profile.SeedArtistCount, profile.SeedSampleCount)
	}
	if profile.TagWeights["solo"] != 0.5 || profile.TagWeights["a"] != 0.125 || profile.TagWeights["b"] != 0.125 {
		t.Fatalf("artist-balanced weights = %#v, want solo .5 and four seed tags .125 each", profile.TagWeights)
	}
	if _, ok := profile.TagWeights["candidate-only"]; ok {
		t.Fatal("pending, non-followed recommendation sample entered the seed profile")
	}
	if _, ok := profile.TagWeights["blocked-only"]; ok {
		t.Fatal("blocked seed entered the profile")
	}
	if profile.NegativeTagWeights["bad"] != 1 {
		t.Fatalf("normalized negative weights = %#v, want bad:1", profile.NegativeTagWeights)
	}
	var sum float64
	for _, weight := range profile.TagWeights {
		sum += weight
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("profile weights sum to %f, want 1", sum)
	}
}

func TestBuildProfileAppliesSeedAndSampleBudgets(t *testing.T) {
	first := SeedArtist{ArtistID: 1, ReviewStatus: "approved"}
	for id := int64(1); id <= 4; id++ {
		first.Samples = append(first.Samples, domain.Artwork{ID: id, ArtistID: 1, Tags: []string{"first"}})
	}
	second := SeedArtist{ArtistID: 2, RemoteFollowed: true}
	second.Samples = []domain.Artwork{{ID: 5, ArtistID: 2, Tags: []string{"second"}}}
	budget := Budget{SeedArtists: 1, SeedSamplesPerArtist: 2}
	profile := BuildProfile([]SeedArtist{second, first}, nil, budget)
	if profile.SeedArtistCount != 1 || profile.SeedSampleCount != 2 || !reflect.DeepEqual(profile.TagWeights, map[string]float64{"first": 1}) {
		t.Fatalf("budgeted profile = %+v, want first artist with two capped samples", profile)
	}
}

func TestScoreCandidateComponentsReasonsAndStringSampleIDs(t *testing.T) {
	profile := Profile{
		TagWeights:         map[string]float64{" blue ": 0.6, "red": 0.4},
		NegativeTagWeights: map[string]float64{"bad": 1},
		SeedArtistCount:    12,
		SeedSampleCount:    30,
	}
	candidate := Candidate{
		ArtistID: 77,
		Sources:  []string{"related_artist", " RELATED_ARTIST ", "tag_search", "future_source"},
		Samples: []domain.Artwork{
			{ID: 40, Tags: []string{"BLUE", "bad"}, Bookmarks: int64ptr(0)},
			{ID: 20, Bookmarks: int64ptr(100)},
			{ID: 30, Bookmarks: int64ptr(10000)},
			{ID: 10, Bookmarks: int64ptr(1000)},
			{ID: 50, Tags: []string{"red"}, Bookmarks: int64ptr(999999)},
		},
	}
	result := ScoreCandidate(profile, candidate, Budget{CandidateSamplesPerArtist: 4})
	median := int64(550)
	quality := math.Log1p(float64(median)) / math.Log1p(qualityBookmarkScale)
	wantScore := clamp(0.55*0.6 + 0.25*0.5 + 0.20*quality - 0.20)
	if result.Version != ScoreVersion || math.Abs(result.Score-wantScore) > 1e-12 {
		t.Fatalf("version/score = %q/%0.12f, want %q/%0.12f", result.Version, result.Score, ScoreVersion, wantScore)
	}
	if result.Components.SourceEvidence != 0.5 || result.Components.TagMatch != 0.6 || result.Components.NegativeTagPenalty != 0.2 || result.Components.MedianBookmarks == nil || *result.Components.MedianBookmarks != median {
		t.Fatalf("unexpected components: %+v", result.Components)
	}
	if !reflect.DeepEqual(result.SampleIDs, []string{"10", "20", "30", "40"}) {
		t.Fatalf("sample IDs = %v, want sorted string IDs from four capped samples", result.SampleIDs)
	}
	if result.Confidence != "high" {
		t.Fatalf("confidence = %q, want high", result.Confidence)
	}
	if len(result.Reasons) < 3 {
		t.Fatalf("explanations are incomplete: %v", result.Reasons)
	}
}

func TestScoreCandidateLowConfidenceAndClamp(t *testing.T) {
	profile := Profile{NegativeTagWeights: map[string]float64{"avoid": 1}}
	result := ScoreCandidate(profile, Candidate{Samples: []domain.Artwork{{ID: 8, Tags: []string{"avoid"}}}}, Budget{})
	if result.Score != 0 || result.Confidence != "low" || result.Components.NegativeTagPenalty != negativePenaltyWeight {
		t.Fatalf("low evidence score should clamp to 0 with low confidence: %+v", result)
	}
	if len(result.SampleIDs) != 1 || result.SampleIDs[0] != "8" {
		t.Fatalf("sample ID should be a decimal string: %v", result.SampleIDs)
	}
}

func TestScoreCandidatesMergesSourcesDeduplicatesArtistsAndAppliesBudget(t *testing.T) {
	profile := Profile{TagWeights: map[string]float64{"blue": 1}}
	results := ScoreCandidates(profile, []Candidate{
		{ArtistID: 3, Sources: []string{"related_artist"}, Samples: []domain.Artwork{{ID: 31, Tags: []string{"blue"}}}},
		{ArtistID: 3, Sources: []string{"tag_search", "related_artist"}, Samples: []domain.Artwork{{ID: 32, Tags: []string{"blue"}}}},
		{ArtistID: 4, Sources: []string{"related_artwork"}, Samples: []domain.Artwork{{ID: 41}}},
		{ArtistID: 5, Sources: []string{"recommendation_feed"}, Samples: []domain.Artwork{{ID: 51}}},
	}, Budget{CandidateArtists: 2})
	if len(results) != 2 {
		t.Fatalf("scored %d candidates, want budgeted 2", len(results))
	}
	if results[0].ArtistID != 3 || results[0].Components.SourceEvidence != 0.5 || !reflect.DeepEqual(results[0].SampleIDs, []string{"31", "32"}) {
		t.Fatalf("duplicate candidate sources/samples were not merged: %+v", results[0])
	}
	if results[1].ArtistID != 4 {
		t.Fatalf("candidate budget should retain first two unique artists, got artist %d", results[1].ArtistID)
	}
}

func int64ptr(value int64) *int64 { return &value }
