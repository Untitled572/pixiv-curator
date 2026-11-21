package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
	"sort"
	"strconv"
	"strings"
)

type RecommendationRepository interface {
	StartRecommendation(context.Context, json.RawMessage) (string, string, error)
	RecommendationSeeds(context.Context, int) ([]domain.Artist, error)
	DirectedExcludedWorks(context.Context, map[int64]int64) (map[int64]bool, error)
	RecommendationRun(context.Context, string) (domain.RecommendationRun, error)
	ReserveRecommendationRequest(context.Context, domain.Job, string, int) (bool, error)
	CommitRecommendationRecall(context.Context, domain.Job, string, int64, map[int64]domain.Artist, map[int64][]string, int) error
	RecommendationCandidates(context.Context, string, int64) ([]domain.Recommendation, error)
	CommitRecommendationSamples(context.Context, domain.Job, string, int64, int64, []domain.Artwork, domain.Recommendation) error
	CommitSeedSamples(context.Context, domain.Job, int64, []domain.Artwork) error
	CompleteRecommendation(context.Context, domain.Job, string) error
	RecommendationSamples(context.Context, int64, int) ([]domain.Artwork, error)
	NegativeTags(context.Context) (map[string]float64, error)
	SaveRecommendationProfile(context.Context, domain.Job, string, json.RawMessage) error
	RecommendationProfile(context.Context, string) (json.RawMessage, error)
}
type RecommendationClient interface {
	RelatedArtists(context.Context, int64) ([]pixiv.RawArtist, error)
	RelatedArtworks(context.Context, int64) ([]pixiv.RawArtwork, error)
	ArtistArtworks(context.Context, int64, string) (pixiv.ArtworkPage, error)
}
type RelatedArtistPreviewClient interface {
	RelatedArtistPreviews(context.Context, int64) ([]pixiv.UserPreview, error)
}
type Recommender struct {
	store  RecommendationRepository
	client RecommendationClient
}

func NewRecommender(s RecommendationRepository, c RecommendationClient) *Recommender {
	return &Recommender{store: s, client: c}
}
func (s *Recommender) Start(ctx context.Context) (string, string, error) {
	return s.store.StartRecommendation(ctx, s.defaultRunBudget())
}

// StartWithExposure freezes the current card exposure set with the new run.
func (s *Recommender) StartWithExposure(ctx context.Context, parent string, exposed []int64) (string, string, error) {
	if len(exposed) > 30 || (parent == "" && len(exposed) > 0) {
		return "", "", domain.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(s.defaultRunBudget(), &fields); err != nil {
		return "", "", err
	}
	fields["exposure_parent"], _ = json.Marshal(parent)
	fields["exposure_ids"], _ = json.Marshal(exposed)
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", "", err
	}
	return s.store.StartRecommendation(ctx, raw)
}

func (s *Recommender) Candidates(ctx context.Context, runID string) (domain.RecommendationRun, []domain.Recommendation, error) {
	run, err := s.store.RecommendationRun(ctx, runID)
	if err != nil {
		return run, nil, err
	}
	items, err := s.store.RecommendationCandidates(ctx, runID, 0)
	order := func(item domain.Recommendation) *int {
		var flags struct {
			Order *int `json:"display_order"`
		}
		_ = json.Unmarshal(item.Components, &flags)
		return flags.Order
	}
	sort.Slice(items, func(i, j int) bool {
		if a, b := order(items[i]), order(items[j]); a != nil && b != nil && *a != *b {
			return *a < *b
		}
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ArtistID < items[j].ArtistID
	})
	return run, items, err
}

func (s *Recommender) CandidatesFiltered(ctx context.Context, runID, contentRating, aiType string) (domain.RecommendationRun, []domain.Recommendation, error) {
	if !validRecommendationRating(contentRating) || !validRecommendationAI(aiType) {
		return domain.RecommendationRun{}, nil, domain.ErrInvalid
	}
	run, items, err := s.Candidates(ctx, runID)
	if err != nil {
		return run, items, err
	}
	return run, filterRecommendationCandidates(items, contentRating, aiType), nil
}

func validRecommendationRating(value string) bool {
	return value == "" || value == "all" || value == "adult" || value == "safe" || value == "unknown" || value == "safe_or_unknown" || value == "adult_or_unknown"
}
func validRecommendationAI(value string) bool {
	return value == "" || value == "all" || value == "ai" || value == "non_ai" || value == "unknown" || value == "non_ai_or_unknown" || value == "ai_or_unknown"
}

func (s *Recommender) Handler(ctx context.Context, j domain.Job) error {
	var payload struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.RunID == "" {
		return taskInvalid()
	}
	run, err := s.store.RecommendationRun(ctx, payload.RunID)
	if err != nil {
		return classify(err)
	}
	var budget recommend.Budget
	if json.Unmarshal(run.Budget, &budget) != nil || budget.SeedArtists < 1 || budget.SeedArtists > 30 || budget.SeedSamplesPerArtist < 1 || budget.SeedSamplesPerArtist > 8 || budget.CandidateSamplesPerArtist < 1 || budget.CandidateSamplesPerArtist > 4 || budget.CandidateArtists < 1 || budget.CandidateArtists > 60 || budget.TotalRequests < 1 || budget.TotalRequests > 300 {
		return taskInvalid()
	}
	var plan struct {
		Version      string `json:"plan_version"`
		WeakFeedback bool   `json:"weak_feedback_enabled"`
	}
	_ = json.Unmarshal(run.Budget, &plan)
	recallLimit := budget.RecallArtists
	if recallLimit == 0 {
		recallLimit = budget.CandidateArtists
	}
	if recallLimit < budget.CandidateArtists || recallLimit > 60 || budget.ExplorationArtists < 0 || budget.ExplorationArtists > budget.CandidateArtists {
		return taskInvalid()
	}
	ctx = pixiv.WithAdditionalRequestBudget(ctx, func(ctx context.Context) error {
		allowed, err := s.store.ReserveRecommendationRequest(ctx, j, run.ID, budget.TotalRequests)
		if err != nil {
			return err
		}
		if !allowed {
			return &pixiv.Error{Code: "REQUEST_BUDGET_EXHAUSTED"}
		}
		return nil
	})
	excluded := map[int64]bool{}
	if repo, ok := s.store.(interface {
		RecommendationExcludedArtistIDs(context.Context, string) ([]int64, error)
	}); ok {
		ids, err := repo.RecommendationExcludedArtistIDs(ctx, run.ID)
		if err != nil {
			return classify(err)
		}
		for _, id := range ids {
			excluded[id] = true
		}
	}
	var directed struct {
		Options *DirectedDiscovery `json:"directed"`
		Date    string             `json:"directed_date"`
		Pages   int                `json:"directed_pages"`
	}
	if json.Unmarshal(run.Budget, &directed) != nil {
		return taskInvalid()
	}
	if directed.Options != nil {
		options, valid := directed.Options.normalized()
		if !valid {
			return taskInvalid()
		}
		return s.directedRun(ctx, j, run, budget, options, directed.Date, directed.Pages, excluded)
	}
	if err = s.recallIndependent(ctx, j, run, recallLimit, budget.TotalRequests, excluded); err != nil {
		return err
	}
	seeds, err := s.store.RecommendationSeeds(ctx, budget.SeedArtists)
	if frozen, ok := s.store.(interface {
		RecommendationSeedsForRun(context.Context, string, int) ([]domain.Artist, error)
	}); ok {
		seeds, err = frozen.RecommendationSeedsForRun(ctx, run.ID, budget.SeedArtists)
	}
	if err != nil {
		return classify(err)
	}
	for _, seed := range seeds {
		if seed.ID <= run.SeedCursor {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		samples, err := s.store.RecommendationSamples(ctx, seed.ID, budget.SeedSamplesPerArtist)
		if err != nil {
			return classify(err)
		}
		if len(samples) == 0 {
			ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
			if err != nil {
				return err
			}
			if ok {
				page, err := s.client.ArtistArtworks(ctx, seed.ID, "")
				if err != nil {
					if haltOptional(err) {
						return classify(err)
					}
				} else {
					samples, err = normalizeSamples(page.Artworks, seed.ID, budget.SeedSamplesPerArtist)
					if err != nil {
						return classify(err)
					}
					if err = s.store.CommitSeedSamples(ctx, j, seed.ID, samples); err != nil {
						return classify(err)
					}
				}
			}
		}
		candidates := map[int64]domain.Artist{}
		sources := map[int64][]string{}
		add := func(raw pixiv.RawArtist, source string) {
			if excluded[raw.ID] {
				return
			}
			eventLimit := recallLimit
			if plan.Version == "v2" {
				eventLimit = 600
			}
			if _, exists := candidates[raw.ID]; !exists && len(candidates) >= eventLimit {
				return
			}
			a, err := pixiv.NormalizeArtist(raw)
			if err != nil {
				return
			}
			candidates[a.ID] = a
			sources[a.ID] = append(sources[a.ID], source)
		}
		previewSamples := map[int64][]domain.Artwork{}
		ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
		if err != nil {
			return err
		}
		if ok {
			if previewClient, supported := s.client.(RelatedArtistPreviewClient); supported {
				previews, err := previewClient.RelatedArtistPreviews(ctx, seed.ID)
				if err != nil {
					if haltOptional(err) {
						return classify(err)
					}
				} else {
					for _, preview := range previews {
						add(preview.User, recommend.SourceRelatedArtist)
						if _, accepted := candidates[preview.User.ID]; !accepted {
							continue
						}
						samples, err := normalizeRecommendationPreviewSamples(preview.Illusts, preview.User.ID, budget.CandidateSamplesPerArtist)
						if err != nil {
							return classify(err)
						}
						previewSamples[preview.User.ID] = mergeRecommendationPreviewSamples(previewSamples[preview.User.ID], samples, budget.CandidateSamplesPerArtist)
					}
				}
			} else {
				users, err := s.client.RelatedArtists(ctx, seed.ID)
				if err != nil {
					if haltOptional(err) {
						return classify(err)
					}
				} else {
					for _, raw := range users {
						add(raw, recommend.SourceRelatedArtist)
					}
				}
			}
		}
		relatedSamples := samples
		if plan.Version == "v2" {
			if ageRepo, ok := s.store.(interface {
				RecommendationArtistAge(context.Context, int64) (string, error)
			}); ok {
				age, ageErr := ageRepo.RecommendationArtistAge(ctx, seed.ID)
				if ageErr != nil {
					return classify(ageErr)
				}
				if age == "safe" {
					relatedSamples = nil
					for _, sample := range samples {
						if sample.Rating == "safe" {
							relatedSamples = append(relatedSamples, sample)
						}
					}
				}
			}
		}
		if len(relatedSamples) > 0 {
			ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
			if err != nil {
				return err
			}
			if ok {
				works, err := s.client.RelatedArtworks(ctx, relatedSamples[0].ID)
				if err != nil {
					if haltOptional(err) {
						return classify(err)
					}
				} else {
					for _, raw := range works {
						add(raw.User, recommend.SourceRelatedArtwork)
					}
				}
			}
		}
		if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, seed.ID, candidates, sources, recallLimit); err != nil {
			return classify(err)
		}
		if len(previewSamples) > 0 {
			admitted, err := s.store.RecommendationCandidates(ctx, run.ID, 0)
			if err != nil {
				return classify(err)
			}
			admittedIDs := make(map[int64]bool, len(admitted))
			for _, candidate := range admitted {
				var carry struct {
					Carryover bool `json:"carryover"`
				}
				_ = json.Unmarshal(candidate.Components, &carry)
				if carry.Carryover {
					continue
				}
				admittedIDs[candidate.ArtistID] = true
			}
			ids := make([]int64, 0, len(previewSamples))
			for id := range previewSamples {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				if !admittedIDs[id] || len(previewSamples[id]) == 0 {
					continue
				}
				if err = s.store.CommitSeedSamples(ctx, j, id, previewSamples[id]); err != nil {
					return classify(err)
				}
			}
		}
	}
	// Freeze the profile once so resumed candidate scoring can be replayed offline.
	rawProfile, err := s.store.RecommendationProfile(ctx, run.ID)
	if err != nil {
		return classify(err)
	}
	var profile recommend.Profile
	if string(rawProfile) == "{}" || len(rawProfile) == 0 {
		input := []recommend.SeedArtist{}
		for _, seed := range seeds {
			samples, err := s.store.RecommendationSamples(ctx, seed.ID, budget.SeedSamplesPerArtist)
			if err != nil {
				return classify(err)
			}
			input = append(input, recommend.SeedArtist{ArtistID: seed.ID, ReviewStatus: seed.ReviewStatus, RemoteFollowed: seed.RemoteFollowed, Samples: samples})
		}
		negatives, err := s.store.NegativeTags(ctx)
		if err != nil {
			return classify(err)
		}
		profile = recommend.BuildProfile(input, negatives, budget)
		if plan.Version == "v2" {
			examples := []recommend.SimilarityExample{}
			for _, seed := range input {
				if seed.ReviewStatus != "approved" && !seed.RemoteFollowed {
					continue
				}
				tags := []string{}
				for _, work := range rankRepresentativeSamples(append([]domain.Artwork(nil), seed.Samples...), 3) {
					tags = append(tags, work.Tags...)
				}
				examples = append(examples, recommend.SimilarityExample{Tags: tags, Weight: 1})
			}
			if repo, ok := s.store.(interface {
				RecommendationSimilarityExamples(context.Context, string) ([]recommend.SimilarityExample, error)
			}); ok {
				examples = nil
				feedback, e := repo.RecommendationSimilarityExamples(ctx, run.ID)
				if e != nil {
					return classify(e)
				}
				for _, example := range feedback {
					if !example.Weak || plan.WeakFeedback {
						examples = append(examples, example)
					}
				}
			}
			model := recommend.BuildSimilarityModel(examples)
			profile.Similarity = &model
		}
		if repo, ok := s.store.(interface {
			ImageFeedbackTags(context.Context) (map[string]float64, error)
		}); ok {
			profile.FeedbackTagCounts, err = repo.ImageFeedbackTags(ctx)
			if err != nil {
				return classify(err)
			}
		}
		var mode struct {
			Independent bool `json:"independent_exploration"`
		}
		_ = json.Unmarshal(run.Budget, &mode)
		if mode.Independent {
			if repo, ok := s.store.(interface {
				RecommendationRegressionExamples(context.Context, string) ([]recommend.RegressionExample, error)
			}); ok {
				examples, e := repo.RecommendationRegressionExamples(ctx, run.ID)
				if e != nil {
					return classify(e)
				}
				model := recommend.TrainRegression(examples)
				profile.Regression = &model
			}
		}
		rawProfile, _ = json.Marshal(profile)
		if err = s.store.SaveRecommendationProfile(ctx, j, run.ID, rawProfile); err != nil {
			return classify(err)
		}
	} else if json.Unmarshal(rawProfile, &profile) != nil {
		return taskInvalid()
	}
	candidates, err := s.store.RecommendationCandidates(ctx, run.ID, run.CandidateCursor)
	if err != nil {
		return classify(err)
	}
	for _, candidate := range candidates {
		var carry struct {
			Carryover bool `json:"carryover"`
		}
		_ = json.Unmarshal(candidate.Components, &carry)
		if carry.Carryover {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		sampleLimit := budget.CandidateSamplesPerArtist
		if plan.Version == "v2" {
			sampleLimit = 30
		}
		samples, err := s.store.RecommendationSamples(ctx, candidate.ArtistID, sampleLimit)
		if err != nil {
			return classify(err)
		}
		if len(samples) == 0 || hasIndependentSource(candidate.Sources) && len(samples) < budget.CandidateSamplesPerArtist || plan.Version == "v2" && len(samples) < 8 {
			ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
			if err != nil {
				return err
			}
			if ok {
				page, err := s.client.ArtistArtworks(ctx, candidate.ArtistID, "")
				if err != nil {
					if haltOptional(err) {
						return classify(err)
					}
				} else {
					var fetched []domain.Artwork
					fetched, err = normalizeSamples(page.Artworks, candidate.ArtistID, sampleLimit)
					if err != nil {
						return classify(err)
					}
					samples = mergeRecommendationPreviewSamples(samples, fetched, sampleLimit)
				}
			}
		}
		featureSamples := rankRepresentativeSamples(append([]domain.Artwork(nil), samples...), 3)
		if plan.Version == "v2" {
			known, adult := 0, 0
			safe := []domain.Artwork{}
			for _, work := range samples {
				switch work.Rating {
				case "safe":
					known++
					safe = append(safe, work)
				case "r18", "r18g":
					known++
					adult++
				}
			}
			if known >= 8 && known*4 >= len(samples)*3 && adult == 0 && len(safe) > 0 {
				featureSamples = rankRepresentativeSamples(safe, 3)
			}
		}
		scored := recommend.ScoreCandidate(profile, recommend.Candidate{ArtistID: candidate.ArtistID, Sources: candidate.Sources, Samples: featureSamples}, budget)
		candidate.Score = scored.Score
		candidate.Reasons = scored.Reasons
		candidate.SampleIDs = scored.SampleIDs
		if plan.Version == "v2" {
			candidate.SampleIDs = nil
			for _, work := range featureSamples {
				candidate.SampleIDs = append(candidate.SampleIDs, strconv.FormatInt(work.ID, 10))
			}
		}
		candidate.Confidence = scored.Confidence
		candidate.Version = scored.Version
		candidate.Components, _ = json.Marshal(scored.Components)
		if hasIndependentSource(candidate.Sources) {
			candidate.Score = 0
			candidate.Version = "independent-v1"
			candidate.Reasons = []string{"代表作供你参考"}
		} else if plan.Version == "v2" && profile.Similarity != nil {
			tags := []string{}
			for _, work := range featureSamples {
				tags = append(tags, work.Tags...)
			}
			hits := 0
			if repo, ok := s.store.(interface {
				RecommendationSeedHits(context.Context, string, int64) (int, error)
			}); ok {
				var e error
				hits, e = repo.RecommendationSeedHits(ctx, run.ID, candidate.ArtistID)
				if e != nil {
					return classify(e)
				}
			}
			candidate.Score = profile.Similarity.Score(tags, hits)
			candidate.Version = "similarity-v2"
			candidate.Reasons = []string{"根据代表作标签与相关画师线索排序"}
		} else if profile.Regression != nil {
			tags := []string{}
			for _, work := range samples {
				tags = append(tags, work.Tags...)
			}
			candidate.Score = profile.Regression.Predict(tags, candidate.Sources)
			candidate.Version = "logistic-v1"
			candidate.Reasons = []string{"根据标签和已确认的画师选择推荐"}
			if !profile.Regression.Trained {
				candidate.Reasons = []string{"选择样本不足，使用中性模型与图库类型分布"}
			}
		}
		if err = s.store.CommitRecommendationSamples(ctx, j, run.ID, candidate.ArtistID, candidate.ArtistID, samples, candidate); err != nil {
			return classify(err)
		}
	}
	return classify(s.store.CompleteRecommendation(ctx, j, run.ID))
}
func normalizeRecommendationPreviewSamples(raw []pixiv.RawArtwork, artistID int64, max int) ([]domain.Artwork, error) {
	matching := make([]pixiv.RawArtwork, 0, len(raw))
	for _, artwork := range raw {
		if artwork.User.ID == artistID {
			matching = append(matching, artwork)
		}
	}
	return normalizeSamples(matching, artistID, max)
}

func mergeRecommendationPreviewSamples(existing, incoming []domain.Artwork, max int) []domain.Artwork {
	byID := make(map[int64]domain.Artwork, len(existing)+len(incoming))
	for _, artwork := range append(append([]domain.Artwork(nil), existing...), incoming...) {
		if artwork.ID > 0 {
			byID[artwork.ID] = artwork
		}
	}
	result := make([]domain.Artwork, 0, len(byID))
	for _, work := range byID {
		result = append(result, work)
	}
	return rankRepresentativeSamples(result, max)
}
func (s *Recommender) reserve(ctx context.Context, j domain.Job, run string, max int) (bool, error) {
	ok, err := s.store.ReserveRecommendationRequest(ctx, j, run, max)
	if err != nil {
		return false, classify(err)
	}
	return ok, nil
}
func haltOptional(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var e *pixiv.Error
	return errors.As(err, &e) && (e.Status == 401 || e.Status == 429 || e.Code == "CREDENTIAL_SAVE_FAILED")
}
func normalizeSamples(raw []pixiv.RawArtwork, id int64, max int) ([]domain.Artwork, error) {
	result := []domain.Artwork{}
	for _, work := range raw {
		if work.User.ID != id {
			continue
		}
		a, err := pixiv.NormalizeArtwork(work)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return rankRepresentativeSamples(result, max), nil
}

func rankRepresentativeSamples(works []domain.Artwork, max int) []domain.Artwork {
	value := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	sort.SliceStable(works, func(i, j int) bool {
		if a, b := value(works[i].Bookmarks), value(works[j].Bookmarks); a != b {
			return a > b
		}
		if a, b := value(works[i].PublishedAt), value(works[j].PublishedAt); a != b {
			return a > b
		}
		return works[i].ID < works[j].ID
	})
	if max <= 0 || len(works) <= max {
		return works
	}
	chosen := append([]domain.Artwork(nil), works[:max]...)
	if max >= 3 {
		recent := max - 1
		for i := max; i < len(works); i++ {
			if value(works[i].PublishedAt) > value(works[recent].PublishedAt) {
				recent = i
			}
		}
		chosen[max-1] = works[recent]
	}
	return chosen
}

func (s *Recommender) Latest(ctx context.Context) (domain.RecommendationRun, []domain.Recommendation, error) {
	repository, ok := s.store.(interface {
		LatestRecommendation(context.Context) (string, error)
	})
	if !ok {
		return domain.RecommendationRun{}, nil, domain.ErrNotFound
	}
	id, err := repository.LatestRecommendation(ctx)
	if err != nil {
		return domain.RecommendationRun{}, nil, err
	}
	return s.Candidates(ctx, id)
}

func (s *Recommender) LatestFiltered(ctx context.Context, contentRating, aiType string) (domain.RecommendationRun, []domain.Recommendation, error) {
	if !validRecommendationRating(contentRating) || !validRecommendationAI(aiType) {
		return domain.RecommendationRun{}, nil, domain.ErrInvalid
	}
	run, items, err := s.Latest(ctx)
	if err != nil {
		return run, nil, err
	}
	return run, filterRecommendationCandidates(items, contentRating, aiType), nil
}

func matchesRecommendationCategory(filter, actual string) bool {
	if filter == "" || filter == "all" || filter == actual {
		return true
	}
	known, includesUnknown := strings.CutSuffix(filter, "_or_unknown")
	return includesUnknown && (actual == known || actual == "unknown")
}

func filterRecommendationCandidates(items []domain.Recommendation, contentRating, aiType string) []domain.Recommendation {
	if (contentRating == "" || contentRating == "all") && (aiType == "" || aiType == "all") {
		return items
	}
	filtered := make([]domain.Recommendation, 0, len(items))
	for _, item := range items {
		if matchesRecommendationCategory(contentRating, item.ContentRating) && matchesRecommendationCategory(aiType, item.AIStatus) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Recommender) Refill(ctx context.Context, j domain.Job) error {
	repo, ok := s.store.(interface {
		StartRecommendationRefill(context.Context, domain.Job, json.RawMessage) error
	})
	if !ok {
		return taskInvalid()
	}
	return classify(repo.StartRecommendationRefill(ctx, j, s.defaultRunBudget()))
}
func (s *Recommender) TouchActivity(ctx context.Context) error {
	if repo, ok := s.store.(interface{ TouchRecommendationActivity(context.Context) error }); ok {
		return repo.TouchRecommendationActivity(ctx)
	}
	return nil
}

func (s *Recommender) PrepareRefill(ctx context.Context) error {
	if repo, ok := s.store.(interface{ PrepareRecommendationRefill(context.Context) error }); ok {
		return repo.PrepareRecommendationRefill(ctx)
	}
	return nil
}
