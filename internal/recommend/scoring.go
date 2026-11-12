// Package recommend contains deterministic, offline recommendation scoring.
package recommend

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const (
	ScoreVersion          = "heuristic-v1"
	qualityBookmarkScale  = 10_000
	negativePenaltyWeight = 0.20
)

// Budget limits all recommendation inputs and upstream request work. It is a
// pure value so a job can persist and replay the exact run parameters.
type Budget struct {
	SeedArtists               int `json:"seed_artists"`
	SeedSamplesPerArtist      int `json:"seed_samples_per_artist"`
	CandidateArtists          int `json:"candidate_artists"`
	CandidateSamplesPerArtist int `json:"candidate_samples_per_artist"`
	TotalRequests             int `json:"total_requests"`
}

func DefaultBudget() Budget {
	return Budget{
		SeedArtists: 8, SeedSamplesPerArtist: 4,
		CandidateArtists: 48, CandidateSamplesPerArtist: 2,
		TotalRequests: 120,
	}
}

// SeedArtist is an allowed source of positive tag evidence. A seed must be
// remotely followed or locally approved; rejected and blocked artists never
// contribute, even if they remain remotely followed.
type SeedArtist struct {
	ArtistID       int64
	ReviewStatus   string
	RemoteFollowed bool
	Samples        []domain.Artwork
}

// Candidate contains bounded artwork samples and every recall source that
// produced the candidate. Candidates should already exclude approved,
// rejected, and blocked artists before scoring.
type Candidate struct {
	ArtistID int64
	Sources  []string
	Samples  []domain.Artwork
}

// Profile weights each eligible seed artist equally, regardless of how many
// works or tags that artist contributed.
type Profile struct {
	TagWeights         map[string]float64
	NegativeTagWeights map[string]float64
	SeedArtistCount    int
	SeedSampleCount    int
}

type Components struct {
	TagMatch           float64  `json:"tag_match"`
	SourceEvidence     float64  `json:"source_evidence"`
	Quality            float64  `json:"quality"`
	NegativeTagPenalty float64  `json:"negative_tag_penalty"`
	MatchedTags        []string `json:"matched_tags"`
	Sources            []string `json:"sources"`
	MedianBookmarks    *int64   `json:"median_bookmarks,omitempty"`
}

type ScoreResult struct {
	ArtistID   int64      `json:"artist_id,string"`
	Version    string     `json:"version"`
	Score      float64    `json:"score"`
	Reasons    []string   `json:"reasons"`
	Components Components `json:"components"`
	Confidence string     `json:"confidence"`
	SampleIDs  []string   `json:"sample_ids"`
}

const (
	SourceRelatedArtist      = "related_artist"
	SourceRelatedArtwork     = "related_artwork"
	SourceRecommendationFeed = "recommendation_feed"
	SourceTagSearch          = "tag_search"
)

var supportedSources = []string{
	SourceRelatedArtist,
	SourceRelatedArtwork,
	SourceRecommendationFeed,
	SourceTagSearch,
}

// BuildProfile builds positive tag weights from followed or approved seeds and
// normalizes optional explicit negative-tag weights to a total of one.
func BuildProfile(seeds []SeedArtist, negativeTags map[string]float64, budget Budget) Profile {
	budget = normalizedBudget(budget)
	byArtist := make(map[int64]*seedGroup)
	for _, seed := range seeds {
		if seed.ArtistID <= 0 || seed.ReviewStatus == "blocked" || seed.ReviewStatus == "rejected" {
			continue
		}
		if !seed.RemoteFollowed && seed.ReviewStatus != "approved" {
			continue
		}
		group := byArtist[seed.ArtistID]
		if group == nil {
			group = &seedGroup{samples: make(map[int64]domain.Artwork)}
			byArtist[seed.ArtistID] = group
		}
		for _, sample := range seed.Samples {
			if sample.ID <= 0 || (sample.ArtistID != 0 && sample.ArtistID != seed.ArtistID) {
				continue
			}
			group.samples[sample.ID] = sample
		}
	}

	artistIDs := make([]int64, 0, len(byArtist))
	for id := range byArtist {
		artistIDs = append(artistIDs, id)
	}
	sort.Slice(artistIDs, func(i, j int) bool { return artistIDs[i] < artistIDs[j] })

	profile := Profile{
		TagWeights:         make(map[string]float64),
		NegativeTagWeights: normalizeWeights(negativeTags),
	}
	for _, artistID := range artistIDs {
		if profile.SeedArtistCount >= budget.SeedArtists {
			break
		}
		samples := sortedArtworks(byArtist[artistID].samples)
		if len(samples) > budget.SeedSamplesPerArtist {
			samples = samples[:budget.SeedSamplesPerArtist]
		}
		tagCounts := make(map[string]float64)
		for _, sample := range samples {
			tags := normalizedUniqueTags(sample.Tags)
			if len(tags) == 0 {
				continue
			}
			profile.SeedSampleCount++
			for _, tag := range tags {
				tagCounts[tag]++
			}
		}
		var total float64
		for _, count := range tagCounts {
			total += count
		}
		if total == 0 {
			continue
		}
		profile.SeedArtistCount++
		for tag, count := range tagCounts {
			profile.TagWeights[tag] += count / total
		}
	}
	if profile.SeedArtistCount > 0 {
		for tag := range profile.TagWeights {
			profile.TagWeights[tag] /= float64(profile.SeedArtistCount)
		}
	}
	return profile
}

// ScoreCandidate returns a bounded heuristic ranking value, not a probability.
// Candidate samples and IDs are deduplicated and capped in ascending artwork
// ID order for deterministic offline replay.
func ScoreCandidate(profile Profile, candidate Candidate, budget Budget) ScoreResult {
	budget = normalizedBudget(budget)
	samples := uniqueArtworks(candidate.Samples)
	if len(samples) > budget.CandidateSamplesPerArtist {
		samples = samples[:budget.CandidateSamplesPerArtist]
	}
	result := ScoreResult{
		ArtistID: candidate.ArtistID,
		Version:  ScoreVersion,
		Components: Components{
			MatchedTags: make([]string, 0),
			Sources:     normalizedSources(candidate.Sources),
		},
		Reasons:   make([]string, 0),
		SampleIDs: make([]string, 0, len(samples)),
	}
	for _, sample := range samples {
		if sample.ID > 0 {
			result.SampleIDs = append(result.SampleIDs, strconv.FormatInt(sample.ID, 10))
		}
	}

	candidateTags := make(map[string]struct{})
	bookmarkCounts := make([]int64, 0, len(samples))
	for _, sample := range samples {
		for _, tag := range normalizedUniqueTags(sample.Tags) {
			candidateTags[tag] = struct{}{}
		}
		if sample.Bookmarks != nil && *sample.Bookmarks >= 0 {
			bookmarkCounts = append(bookmarkCounts, *sample.Bookmarks)
		}
	}

	var profileWeight float64
	for tag, weight := range profile.TagWeights {
		if validWeight(weight) {
			profileWeight += weight
			if _, ok := candidateTags[normalize(tag)]; ok {
				result.Components.TagMatch += weight
				result.Components.MatchedTags = append(result.Components.MatchedTags, normalize(tag))
			}
		}
	}
	if profileWeight > 0 {
		result.Components.TagMatch /= profileWeight
		result.Components.TagMatch = clamp(result.Components.TagMatch)
	}
	sort.Strings(result.Components.MatchedTags)
	result.Components.SourceEvidence = clamp(float64(len(result.Components.Sources)) / float64(len(supportedSources)))
	if median, ok := medianInt64(bookmarkCounts); ok {
		result.Components.MedianBookmarks = &median
		result.Components.Quality = clamp(math.Log1p(float64(median)) / math.Log1p(qualityBookmarkScale))
	}
	for tag, weight := range profile.NegativeTagWeights {
		if _, ok := candidateTags[normalize(tag)]; ok && validWeight(weight) {
			result.Components.NegativeTagPenalty += weight
		}
	}
	result.Components.NegativeTagPenalty = clamp(result.Components.NegativeTagPenalty) * negativePenaltyWeight

	result.Score = clamp(
		0.55*result.Components.TagMatch +
			0.25*result.Components.SourceEvidence +
			0.20*result.Components.Quality -
			result.Components.NegativeTagPenalty,
	)
	result.Confidence = confidence(profile, len(samples))
	result.Reasons = scoringReasons(result.Components, len(samples), profile)
	return result
}

// ScoreCandidates merges duplicate artist IDs from independent recall sources,
// scores at most the configured candidate budget and returns highest scores
// first. Ties use artist ID for deterministic replay.
func ScoreCandidates(profile Profile, candidates []Candidate, budget Budget) []ScoreResult {
	budget = normalizedBudget(budget)
	merged := make([]Candidate, 0, len(candidates))
	positions := make(map[int64]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.ArtistID <= 0 {
			continue
		}
		if index, ok := positions[candidate.ArtistID]; ok {
			merged[index].Sources = append(merged[index].Sources, candidate.Sources...)
			merged[index].Samples = append(merged[index].Samples, candidate.Samples...)
			continue
		}
		positions[candidate.ArtistID] = len(merged)
		merged = append(merged, candidate)
	}
	if len(merged) > budget.CandidateArtists {
		merged = merged[:budget.CandidateArtists]
	}
	results := make([]ScoreResult, 0, len(merged))
	for _, candidate := range merged {
		results = append(results, ScoreCandidate(profile, candidate, budget))
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ArtistID < results[j].ArtistID
	})
	return results
}

type seedGroup struct{ samples map[int64]domain.Artwork }

func normalizedBudget(budget Budget) Budget {
	defaults := DefaultBudget()
	if budget.SeedArtists <= 0 {
		budget.SeedArtists = defaults.SeedArtists
	}
	if budget.SeedSamplesPerArtist <= 0 {
		budget.SeedSamplesPerArtist = defaults.SeedSamplesPerArtist
	}
	if budget.CandidateArtists <= 0 {
		budget.CandidateArtists = defaults.CandidateArtists
	}
	if budget.CandidateSamplesPerArtist <= 0 {
		budget.CandidateSamplesPerArtist = defaults.CandidateSamplesPerArtist
	}
	if budget.TotalRequests <= 0 {
		budget.TotalRequests = defaults.TotalRequests
	}
	return budget
}

func sortedArtworks(byID map[int64]domain.Artwork) []domain.Artwork {
	result := make([]domain.Artwork, 0, len(byID))
	for _, artwork := range byID {
		result = append(result, artwork)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func uniqueArtworks(input []domain.Artwork) []domain.Artwork {
	byID := make(map[int64]domain.Artwork, len(input))
	for _, artwork := range input {
		if artwork.ID > 0 {
			byID[artwork.ID] = artwork
		}
	}
	return sortedArtworks(byID)
}

func normalizedUniqueTags(tags []string) []string {
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if tag = normalize(tag); tag != "" {
			set[tag] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for tag := range set {
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func normalizedSources(sources []string) []string {
	known := make(map[string]struct{}, len(supportedSources))
	for _, source := range supportedSources {
		known[source] = struct{}{}
	}
	set := make(map[string]struct{})
	for _, source := range sources {
		source = normalize(source)
		if _, ok := known[source]; ok {
			set[source] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for source := range set {
		result = append(result, source)
	}
	sort.Strings(result)
	return result
}

func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizeWeights(weights map[string]float64) map[string]float64 {
	clean := make(map[string]float64)
	var total float64
	for tag, weight := range weights {
		tag = normalize(tag)
		if tag == "" || !validWeight(weight) || weight <= 0 {
			continue
		}
		clean[tag] += weight
		total += weight
	}
	if total > 0 {
		for tag := range clean {
			clean[tag] /= total
		}
	}
	return clean
}

func validWeight(weight float64) bool {
	return !math.IsNaN(weight) && !math.IsInf(weight, 0) && weight >= 0
}

func medianInt64(values []int64) (int64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle], true
	}
	return values[middle-1]/2 + values[middle]/2 + (values[middle-1]%2+values[middle]%2)/2, true
}

func confidence(profile Profile, candidateSamples int) string {
	if candidateSamples < 4 || profile.SeedArtistCount < 3 || profile.SeedSampleCount < 8 {
		return "low"
	}
	if profile.SeedArtistCount < 10 || profile.SeedSampleCount < 24 {
		return "medium"
	}
	return "high"
}

func scoringReasons(components Components, candidateSamples int, profile Profile) []string {
	reasons := make([]string, 0, 4)
	if len(components.MatchedTags) > 0 {
		reasons = append(reasons, "matched tags: "+strings.Join(components.MatchedTags, ", "))
	}
	if len(components.Sources) > 0 {
		reasons = append(reasons, "recall sources: "+strings.Join(components.Sources, ", "))
	}
	if components.MedianBookmarks != nil {
		reasons = append(reasons, "median bookmarks: "+strconv.FormatInt(*components.MedianBookmarks, 10))
	}
	if components.NegativeTagPenalty > 0 {
		reasons = append(reasons, "matches explicit negative tags")
	}
	if candidateSamples < 4 || profile.SeedArtistCount < 3 || profile.SeedSampleCount < 8 {
		reasons = append(reasons, "low confidence: sample count below the initial evidence threshold")
	}
	return reasons
}

func clamp(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
