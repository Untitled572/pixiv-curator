package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
	"sort"
)

type RecommendationRepository interface {
	StartRecommendation(context.Context, json.RawMessage) (string, string, error)
	RecommendationSeeds(context.Context, int) ([]domain.Artist, error)
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
type Recommender struct {
	store  RecommendationRepository
	client RecommendationClient
}

func NewRecommender(s RecommendationRepository, c RecommendationClient) *Recommender {
	return &Recommender{store: s, client: c}
}
func (s *Recommender) Start(ctx context.Context) (string, string, error) {
	b, _ := json.Marshal(recommend.DefaultBudget())
	return s.store.StartRecommendation(ctx, b)
}
func (s *Recommender) Candidates(ctx context.Context, runID string) (domain.RecommendationRun, []domain.Recommendation, error) {
	run, err := s.store.RecommendationRun(ctx, runID)
	if err != nil {
		return run, nil, err
	}
	items, err := s.store.RecommendationCandidates(ctx, runID, 0)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ArtistID < items[j].ArtistID
	})
	return run, items, err
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
	seeds, err := s.store.RecommendationSeeds(ctx, budget.SeedArtists)
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
			if _, exists := candidates[raw.ID]; !exists && len(candidates) >= budget.CandidateArtists {
				return
			}
			a, err := pixiv.NormalizeArtist(raw)
			if err != nil {
				return
			}
			candidates[a.ID] = a
			sources[a.ID] = append(sources[a.ID], source)
		}
		ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
		if err != nil {
			return err
		}
		if ok {
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
		if len(samples) > 0 {
			ok, err := s.reserve(ctx, j, run.ID, budget.TotalRequests)
			if err != nil {
				return err
			}
			if ok {
				works, err := s.client.RelatedArtworks(ctx, samples[0].ID)
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
		if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, seed.ID, candidates, sources, budget.CandidateArtists); err != nil {
			return classify(err)
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
		if err = ctx.Err(); err != nil {
			return err
		}
		samples, err := s.store.RecommendationSamples(ctx, candidate.ArtistID, budget.CandidateSamplesPerArtist)
		if err != nil {
			return classify(err)
		}
		if len(samples) == 0 {
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
					samples, err = normalizeSamples(page.Artworks, candidate.ArtistID, budget.CandidateSamplesPerArtist)
					if err != nil {
						return classify(err)
					}
				}
			}
		}
		scored := recommend.ScoreCandidate(profile, recommend.Candidate{ArtistID: candidate.ArtistID, Sources: candidate.Sources, Samples: samples}, budget)
		candidate.Score = scored.Score
		candidate.Reasons = scored.Reasons
		candidate.SampleIDs = scored.SampleIDs
		candidate.Confidence = scored.Confidence
		candidate.Version = scored.Version
		candidate.Components, _ = json.Marshal(scored.Components)
		if err = s.store.CommitRecommendationSamples(ctx, j, run.ID, candidate.ArtistID, candidate.ArtistID, samples, candidate); err != nil {
			return classify(err)
		}
	}
	return classify(s.store.CompleteRecommendation(ctx, j, run.ID))
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
		if len(result) >= max {
			break
		}
		if work.User.ID != id {
			continue
		}
		a, err := pixiv.NormalizeArtwork(work)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}
