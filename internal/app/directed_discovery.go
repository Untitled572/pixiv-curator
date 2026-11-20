package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
)

const (
	legacyDirectedPages  = 6
	directedSearchPages  = 16
	directedRankingPages = 24
	directedRequestLimit = 160
	directedSource       = "directed_random"
)

type DirectedDiscovery struct {
	Tags        []string `json:"tags"`
	Rating      string   `json:"rating"`
	AI          string   `json:"ai"`
	Orientation string   `json:"orientation"`
}
type directedSearchClient interface {
	SearchTaggedArtworks(context.Context, string, int) ([]pixiv.RawArtwork, error)
}

func (in DirectedDiscovery) normalized() (DirectedDiscovery, bool) {
	if !validRecommendationRating(in.Rating) || !validRecommendationAI(in.AI) || len(in.Tags) > 5 {
		return in, false
	}
	switch in.Orientation {
	case "", "any", "landscape", "portrait", "square":
	default:
		return in, false
	}
	tags := make([]string, 0, len(in.Tags))
	for _, raw := range in.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || utf8.RuneCountInString(tag) > 100 || strings.ContainsFunc(tag, unicode.IsControl) {
			return in, false
		}
		exists := false
		for _, other := range tags {
			exists = exists || strings.EqualFold(tag, other)
		}
		if !exists {
			tags = append(tags, tag)
		}
	}
	in.Tags = tags
	if in.Rating == "" {
		in.Rating = "all"
	}
	if in.AI == "" {
		in.AI = "all"
	}
	if in.Orientation == "" {
		in.Orientation = "any"
	}
	return in, len(tags) > 0 || in.Rating != "all" || in.AI != "all" || in.Orientation != "any"
}

func (s *Recommender) StartDirected(ctx context.Context, options DirectedDiscovery) (string, string, error) {
	options, ok := options.normalized()
	if !ok {
		return "", "", domain.ErrInvalid
	}
	if _, ok := s.client.(directedSearchClient); len(options.Tags) > 0 && !ok {
		return "", "", domain.ErrInvalid
	}
	if _, ok := s.client.(independentExplorationClient); len(options.Tags) == 0 && !ok {
		return "", "", domain.ErrInvalid
	}
	base := recommend.DefaultBudget()
	base.ExplorationArtists = 0
	base.TotalRequests = directedRequestLimit
	pages := directedRankingPages
	if len(options.Tags) > 0 {
		pages = directedSearchPages
	}
	budget, _ := json.Marshal(struct {
		recommend.Budget
		Directed DirectedDiscovery `json:"directed"`
		Date     string            `json:"directed_date"`
		Pages    int               `json:"directed_pages"`
	}{base, options, time.Now().UTC().AddDate(0, 0, -2).Format(time.DateOnly), pages})
	return s.store.StartRecommendation(ctx, budget)
}

func (in DirectedDiscovery) matches(work domain.Artwork) bool {
	if in.Rating == "safe" && work.Rating != "safe" ||
		in.Rating == "adult" && work.Rating != "r18" && work.Rating != "r18g" ||
		in.Rating == "unknown" && work.Rating != "unknown" ||
		in.AI != "all" && work.AIStatus != in.AI {
		return false
	}
	for _, required := range in.Tags {
		found := false
		for _, tag := range work.Tags {
			if strings.EqualFold(strings.TrimSpace(tag), required) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func workOrientation(work domain.Artwork) string {
	if len(work.Pages) == 0 || work.Pages[0].Width <= 0 || work.Pages[0].Height <= 0 {
		return ""
	}
	w, h := int64(work.Pages[0].Width), int64(work.Pages[0].Height)
	if w*10 > h*11 {
		return "landscape"
	}
	if h*10 > w*11 {
		return "portrait"
	}
	return "square"
}

func (in DirectedDiscovery) matchesPreview(work domain.Artwork) bool {
	return in.matches(work) && (in.Orientation == "any" || workOrientation(work) == in.Orientation)
}

// The latest works serve both the orientation check and the supplemental previews.
func (s *Recommender) directedRecentWorks(ctx context.Context, j domain.Job, runID string, total int, id int64) ([]domain.Artwork, error) {
	ok, err := s.reserve(ctx, j, runID, total)
	if err != nil || !ok {
		return nil, err
	}
	page, err := s.client.ArtistArtworks(ctx, id, "")
	if err != nil {
		if haltOptional(err) {
			return nil, classify(err)
		}
		return nil, nil
	}
	works := make([]domain.Artwork, 0, len(page.Artworks))
	owners := make(map[int64]int64, len(page.Artworks))
	for _, raw := range page.Artworks {
		_, work, valid := normalizeIndependentArtwork(raw, raw.ID)
		if !valid || work.ArtistID != id || owners[work.ID] != 0 {
			continue
		}
		owners[work.ID] = id
		works = append(works, work)
	}
	excluded, err := s.store.DirectedExcludedWorks(ctx, owners)
	if err != nil {
		return nil, classify(err)
	}
	return slices.DeleteFunc(works, func(work domain.Artwork) bool { return excluded[work.ID] }), nil
}

func majorityOrientation(works []domain.Artwork, want string) bool {
	known, matched := 0, 0
	for _, work := range works {
		direction := workOrientation(work)
		if direction == "" {
			continue
		}
		known++
		if direction == want {
			matched++
		}
		if known == 8 {
			break
		}
	}
	return known >= 2 && matched*2 > known
}

func (s *Recommender) directedRun(ctx context.Context, j domain.Job, run domain.RecommendationRun, budget recommend.Budget, options DirectedDiscovery, date string, pages int, excluded map[int64]bool) error {
	search, canSearch := s.client.(directedSearchClient)
	ranking, canRank := s.client.(independentExplorationClient)
	now, err := time.Parse(time.DateOnly, date)
	if err != nil || (len(options.Tags) > 0 && !canSearch) || (len(options.Tags) == 0 && !canRank) {
		return taskInvalid()
	}
	maxPages := directedRankingPages
	if len(options.Tags) > 0 {
		maxPages = directedSearchPages
	}
	if pages == 0 {
		pages = legacyDirectedPages
	}
	if pages < 1 || pages > maxPages {
		return taskInvalid()
	}
	hash := sha256.Sum256([]byte(run.ID))
	rng := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(hash[:8]), binary.LittleEndian.Uint64(hash[8:16])))
	offsets := rng.Perm(pages)
	// Always include the newest search page before sampling other offsets.
	for i, offset := range offsets {
		if offset == 0 {
			offsets[0], offsets[i] = offsets[i], offsets[0]
			break
		}
	}
	dates := make([]int, pages)
	for i := range dates {
		dates[i] = rng.IntN(365)
	}
	items, err := s.store.RecommendationCandidates(ctx, run.ID, 0)
	if err != nil {
		return classify(err)
	}
	seen := make(map[int64]bool)
	count := 0
	for _, item := range items {
		if len(item.SampleIDs) > 0 {
			seen[item.ArtistID] = true
			count++
		}
	}

	for i := int(run.SeedCursor); i < pages && count < budget.CandidateArtists; i++ {
		ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		var works []pixiv.RawArtwork
		if len(options.Tags) > 0 {
			works, err = search.SearchTaggedArtworks(ctx, options.Tags[0], offsets[i]*30)
		} else {
			mode := "day"
			if options.Rating == "adult" || (options.Rating != "safe" && i%2 == 1) {
				mode = "day_r18"
			}
			offset := offsets[i] * 30
			if pages > legacyDirectedPages {
				// Draw shallow pages across more dates instead of deep pages with few entries.
				offset = (offsets[i] % 3) * 30
			}
			works, err = ranking.ExplorationArtworks(ctx, mode, now.AddDate(0, 0, -dates[i]).Format(time.DateOnly), offset)
		}
		if err != nil {
			var upstream *pixiv.Error
			if errors.As(err, &upstream) && upstream.Code == "REQUEST_BUDGET_EXHAUSTED" {
				break
			}
			return classify(err)
		}
		shuffle := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(hash[16:24]), uint64(i)))
		shuffle.Shuffle(len(works), func(a, b int) { works[a], works[b] = works[b], works[a] })
		workOwners := make(map[int64]int64, len(works))
		for _, raw := range works {
			workOwners[raw.ID] = raw.User.ID
		}
		ineligible, err := s.store.DirectedExcludedWorks(ctx, workOwners)
		if err != nil {
			return classify(err)
		}
		samples := make(map[int64][]domain.Artwork)
		artists := make(map[int64]domain.Artist)
		var order []int64
		for _, raw := range works {
			artist, work, valid := normalizeIndependentArtwork(raw, raw.ID)
			if !valid || ineligible[work.ID] || seen[artist.ID] || excluded[artist.ID] || !options.matchesPreview(work) {
				continue
			}
			if len(samples[artist.ID]) == 0 {
				order = append(order, artist.ID)
			}
			artists[artist.ID] = artist
			samples[artist.ID] = append(samples[artist.ID], work)
		}
		for _, id := range order {
			if count >= budget.CandidateArtists {
				break
			}
			seen[id] = true
			chosen := mergeRecommendationPreviewSamples(nil, samples[id], budget.CandidateSamplesPerArtist)
			if options.Orientation != "any" || len(chosen) < budget.CandidateSamplesPerArtist {
				recent, err := s.directedRecentWorks(ctx, j, run.ID, budget.TotalRequests, id)
				if err != nil {
					return err
				}
				if options.Orientation != "any" && !majorityOrientation(recent, options.Orientation) {
					continue
				}
				matching := slices.DeleteFunc(recent, func(work domain.Artwork) bool { return !options.matchesPreview(work) })
				chosen = mergeRecommendationPreviewSamples(chosen, matching, budget.CandidateSamplesPerArtist)
			}
			sources := []string{directedSource}
			if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, int64(i), map[int64]domain.Artist{id: artists[id]}, map[int64][]string{id: sources}, 60); err != nil {
				return classify(err)
			}
			// Only the store can admit a new pending artist. Existing, blocked and
			// rejected artists are excluded at admission, including concurrent changes.
			admitted, err := s.store.RecommendationCandidates(ctx, run.ID, id-1)
			if err != nil {
				return classify(err)
			}
			if len(admitted) == 0 || admitted[0].ArtistID != id {
				continue
			}
			ids := make([]string, 0, len(chosen))
			for _, work := range chosen {
				ids = append(ids, strconv.FormatInt(work.ID, 10))
			}
			components, _ := json.Marshal(map[string]any{"display_order": count})
			item := domain.Recommendation{ArtistID: id, Sources: sources, SampleIDs: ids, Version: "directed-v1", Components: components, Confidence: "low"}
			if err = s.store.CommitRecommendationSamples(ctx, j, run.ID, id, 0, chosen, item); err != nil {
				return classify(err)
			}
			count++
		}
		if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, int64(i+1), nil, nil, 60); err != nil {
			return classify(err)
		}
	}
	return classify(s.store.CompleteRecommendation(ctx, j, run.ID))
}
