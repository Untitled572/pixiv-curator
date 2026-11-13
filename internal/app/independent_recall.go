package app

import (
	"context"
	"encoding/json"
	rand "math/rand/v2"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
)

const independentRecallSource = "independent_ranking"
const independentUniformSource = "independent_uniform_id"

type independentExplorationClient interface {
	ExplorationArtworks(context.Context, string, string, int) ([]pixiv.RawArtwork, error)
}

type independentUniformClient interface {
	Artwork(context.Context, int64) (pixiv.RawArtwork, error)
}

type independentExplorationCheckpoint interface {
	AdvanceExploration(context.Context, domain.Job, string, int, int, int64) error
}

func (s *Recommender) defaultRunBudget() json.RawMessage {
	raw, _ := json.Marshal(recommend.DefaultBudget())
	if _, ok := s.client.(independentExplorationClient); !ok {
		return raw
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["independent_exploration"] = json.RawMessage("true")
	raw, _ = json.Marshal(fields)
	return raw
}

func hasIndependentSource(sources []string) bool {
	for _, source := range sources {
		if source == independentRecallSource || source == independentUniformSource {
			return true
		}
	}
	return false
}

func (s *Recommender) recallIndependent(ctx context.Context, j domain.Job, run domain.RecommendationRun, limit, total int, excluded map[int64]bool) error {
	var b struct {
		Enabled         bool    `json:"independent_exploration"`
		Cursor          int     `json:"exploration_cursor"`
		UniformCursor   int     `json:"uniform_cursor"`
		RankingMax      int64   `json:"ranking_max_illust_id"`
		UniformTarget   int     `json:"uniform_sample_target"`
		RankingLimit    int     `json:"ranking_candidate_limit"`
		UniformProbeIDs []int64 `json:"uniform_probe_ids"`
		Queries         []struct {
			Mode   string `json:"mode"`
			Date   string `json:"date"`
			Offset int    `json:"offset"`
			Seed   int64  `json:"shuffle_seed"`
		} `json:"exploration_queries"`
	}
	if json.Unmarshal(run.Budget, &b) != nil || b.Cursor < 0 || b.UniformCursor < 0 || b.UniformTarget < 0 || len(b.Queries) > 4 {
		return taskInvalid()
	}
	if !b.Enabled {
		return nil
	}
	client, ok := s.client.(independentExplorationClient)
	if !ok {
		return taskInvalid()
	}
	checkpoint, ok := s.store.(independentExplorationCheckpoint)
	if !ok {
		return taskInvalid()
	}
	for i, query := range b.Queries {
		if i < b.Cursor {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		allowed, err := s.reserve(ctx, j, run.ID, total)
		if err != nil {
			return err
		}
		if !allowed {
			break
		}
		works, err := client.ExplorationArtworks(ctx, query.Mode, query.Date, query.Offset)
		if err != nil {
			if haltOptional(err) {
				return classify(err)
			}
			if err = checkpoint.AdvanceExploration(ctx, j, run.ID, i+1, -1, 0); err != nil {
				return classify(err)
			}
			continue
		}
		rankingMax := int64(0)
		if i == 0 && query.Mode == "day" && query.Offset == 0 {
			for _, work := range works {
				if work.ID > rankingMax {
					rankingMax = work.ID
				}
			}
		}
		// The current page supplies only the public ID bound; allowing it into
		// the cohort first would crowd out all randomly dated ranking pages.
		if i == 0 {
			if err = checkpoint.AdvanceExploration(ctx, j, run.ID, i+1, -1, rankingMax); err != nil {
				return classify(err)
			}
			continue
		}
		random := rand.New(rand.NewPCG(uint64(query.Seed), uint64(i+1)))
		random.Shuffle(len(works), func(i, j int) { works[i], works[j] = works[j], works[i] })
		artists := map[int64]domain.Artist{}
		sources := map[int64][]string{}
		samples := map[int64][]domain.Artwork{}
		pageLimit := b.RankingLimit
		if i < len(b.Queries)-1 {
			pageLimit = max(1, b.RankingLimit/max(1, len(b.Queries)-1))
		}
		for _, raw := range works {
			if len(artists) >= pageLimit {
				break
			}
			artist, sample, valid := normalizeIndependentArtwork(raw, raw.ID)
			if !valid || excluded[artist.ID] {
				continue
			}
			artists[artist.ID] = artist
			sources[artist.ID] = []string{independentRecallSource}
			samples[artist.ID] = append(samples[artist.ID], sample)
		}
		if len(artists) > 0 {
			if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, run.SeedCursor, artists, sources, limit); err != nil {
				return classify(err)
			}
			if err = s.commitIndependentSamples(ctx, j, run.ID, samples, independentRecallSource); err != nil {
				return err
			}
		}
		if err = checkpoint.AdvanceExploration(ctx, j, run.ID, i+1, -1, rankingMax); err != nil {
			return classify(err)
		}
	}
	// Query the saved plan after ranking checkpoints: the first day page may
	// have atomically created the frozen uniform probe IDs.
	run, err := s.store.RecommendationRun(ctx, run.ID)
	if err != nil {
		return classify(err)
	}
	if json.Unmarshal(run.Budget, &b) != nil {
		return taskInvalid()
	}
	uniformClient, hasArtwork := s.client.(independentUniformClient)
	if !hasArtwork || b.UniformTarget == 0 || len(b.UniformProbeIDs) == 0 {
		return nil
	}
	if b.UniformCursor > len(b.UniformProbeIDs) {
		return taskInvalid()
	}
	for i := b.UniformCursor; i < len(b.UniformProbeIDs); i++ {
		admitted, err := s.store.RecommendationCandidates(ctx, run.ID, 0)
		if err != nil {
			return classify(err)
		}
		if uniformArtistCount(admitted) >= b.UniformTarget {
			if err = checkpoint.AdvanceExploration(ctx, j, run.ID, -1, len(b.UniformProbeIDs), 0); err != nil {
				return classify(err)
			}
			break
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		allowed, err := s.reserve(ctx, j, run.ID, total)
		if err != nil {
			return err
		}
		if !allowed {
			break
		}
		illustID := b.UniformProbeIDs[i]
		raw, fetchErr := uniformClient.Artwork(ctx, illustID)
		if fetchErr != nil {
			if haltOptional(fetchErr) {
				return classify(fetchErr)
			}
			if err = checkpoint.AdvanceExploration(ctx, j, run.ID, -1, i+1, 0); err != nil {
				return classify(err)
			}
			continue
		}
		artist, sample, valid := normalizeIndependentArtwork(raw, illustID)
		if valid && !excluded[artist.ID] {
			if err = s.store.CommitRecommendationRecall(ctx, j, run.ID, run.SeedCursor, map[int64]domain.Artist{artist.ID: artist}, map[int64][]string{artist.ID: {independentUniformSource}}, limit); err != nil {
				return classify(err)
			}
			if err = s.commitIndependentSamples(ctx, j, run.ID, map[int64][]domain.Artwork{artist.ID: {sample}}, independentUniformSource); err != nil {
				return err
			}
		}
		if err = checkpoint.AdvanceExploration(ctx, j, run.ID, -1, i+1, 0); err != nil {
			return classify(err)
		}
	}
	return nil
}

func normalizeIndependentArtwork(raw pixiv.RawArtwork, requestedID int64) (domain.Artist, domain.Artwork, bool) {
	if requestedID <= 0 || raw.ID != requestedID || (raw.Type != "illust" && raw.Type != "manga") {
		return domain.Artist{}, domain.Artwork{}, false
	}
	artist, err := pixiv.NormalizeArtist(raw.User)
	if err != nil {
		return domain.Artist{}, domain.Artwork{}, false
	}
	work, err := pixiv.NormalizeArtwork(raw)
	if err != nil || work.ArtistID != artist.ID || work.MetadataState != "complete" || len(work.Pages) == 0 || work.Pages[0].State != "indexed" {
		return domain.Artist{}, domain.Artwork{}, false
	}
	return artist, work, true
}

func (s *Recommender) commitIndependentSamples(ctx context.Context, j domain.Job, runID string, samples map[int64][]domain.Artwork, source string) error {
	admitted, err := s.store.RecommendationCandidates(ctx, runID, 0)
	if err != nil {
		return classify(err)
	}
	for _, candidate := range admitted {
		var carry struct {
			Carryover bool `json:"carryover"`
		}
		_ = json.Unmarshal(candidate.Components, &carry)
		if !carry.Carryover && containsRecommendationSource(candidate.Sources, source) && len(samples[candidate.ArtistID]) > 0 {
			if err = s.store.CommitSeedSamples(ctx, j, candidate.ArtistID, rankRepresentativeSamples(samples[candidate.ArtistID], 3)); err != nil {
				return classify(err)
			}
		}
	}
	return nil
}

func uniformArtistCount(candidates []domain.Recommendation) int {
	seen := map[int64]bool{}
	for _, candidate := range candidates {
		var carry struct {
			Carryover bool `json:"carryover"`
		}
		_ = json.Unmarshal(candidate.Components, &carry)
		if !carry.Carryover && containsRecommendationSource(candidate.Sources, independentUniformSource) {
			seen[candidate.ArtistID] = true
		}
	}
	return len(seen)
}

func containsRecommendationSource(sources []string, want string) bool {
	for _, source := range sources {
		if source == want {
			return true
		}
	}
	return false
}
