package sqlite

import (
	"context"
	"encoding/json"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// recommendationProgress returns a stable, phase-based estimate for a run.
// Request counts are intentionally excluded because cache hits change request
// volume without changing the amount of recommendation work completed.
func (s *Store) recommendationProgress(ctx context.Context, run domain.RecommendationRun) (int, error) {
	if run.State == "pending" {
		return 0, nil
	}
	if run.State == "succeeded" {
		return 100, nil
	}

	var stats string
	if err := s.db.QueryRowContext(ctx, `SELECT stats_json FROM recommendation_runs WHERE id=?`, run.ID).Scan(&stats); err != nil {
		return 0, err
	}
	if stats == "{}" {
		var budget struct {
			SeedArtists   int     `json:"seed_artists"`
			SeedArtistIDs []int64 `json:"seed_artist_ids"`
		}
		if err := json.Unmarshal(run.Budget, &budget); err != nil {
			return 0, err
		}
		seedIDs := budget.SeedArtistIDs
		if seedIDs == nil {
			limit := budget.SeedArtists
			if limit < 1 {
				limit = 8
			}
			seeds, err := s.RecommendationSeeds(ctx, limit)
			if err != nil {
				return 0, err
			}
			seedIDs = make([]int64, 0, len(seeds))
			for _, seed := range seeds {
				seedIDs = append(seedIDs, seed.ID)
			}
		}
		if run.SeedCursor <= 0 {
			return 1, nil
		}
		if len(seedIDs) == 0 {
			return 40, nil
		}
		done := 0
		for _, id := range seedIDs {
			if id <= run.SeedCursor {
				done++
			}
		}
		return min(40, 5+35*done/len(seedIDs)), nil
	}

	var total, processed int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN score_version<>'' THEN 1 ELSE 0 END),0) FROM recommendations WHERE run_id=?`, run.ID).Scan(&total, &processed); err != nil {
		return 0, err
	}
	if total == 0 {
		return 95, nil
	}
	percent := 40 + 55*processed/total
	return min(95, percent), nil
}
