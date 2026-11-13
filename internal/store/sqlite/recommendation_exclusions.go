package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// freezeRecommendationExclusions stores the immediately previous run's
// candidates in the new run budget so retries keep the same exclusion set.
func freezeRecommendationExclusions(ctx context.Context, tx *sql.Tx, budget json.RawMessage, parentRunID string) (json.RawMessage, error) {
	if len(budget) == 0 || !json.Valid(budget) {
		return nil, domain.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(budget, &fields); err != nil {
		return nil, domain.ErrInvalid
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	if parentRunID == "" {
		err := tx.QueryRowContext(ctx, `SELECT id FROM recommendation_runs WHERE state='succeeded' ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&parentRunID)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == sql.ErrNoRows {
			parentRunID = ""
		}
	}
	ids := []int64{}
	if parentRunID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT artist_id FROM recommendations WHERE run_id=? ORDER BY artist_id`, parentRunID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	fields["excluded_artist_ids"] = encoded
	return json.Marshal(fields)
}

func recommendationExclusionSet(budget json.RawMessage) (map[int64]bool, error) {
	var fields struct {
		ExcludedArtistIDs []int64 `json:"excluded_artist_ids"`
	}
	if len(budget) == 0 || json.Unmarshal(budget, &fields) != nil {
		return nil, domain.ErrInvalid
	}
	set := make(map[int64]bool, len(fields.ExcludedArtistIDs))
	for _, id := range fields.ExcludedArtistIDs {
		if id <= 0 {
			return nil, domain.ErrInvalid
		}
		set[id] = true
	}
	return set, nil
}

// RecommendationExcludedArtistIDs exposes the run-frozen exclusion set to the
// discovery application so it can avoid filling its bounded recall pool with
// candidates that the store will discard at commit time.
func (s *Store) RecommendationExcludedArtistIDs(ctx context.Context, runID string) ([]int64, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	set, err := recommendationExclusionSet(json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}
