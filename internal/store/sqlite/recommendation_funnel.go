package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"strconv"
)

type recommendationFunnelItem struct {
	ArtistID int64
	Source   string
	Valid    bool
	Samples  string
}

func recordRecommendationFunnel(ctx context.Context, tx *sql.Tx, runID string, after bool) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return domain.ErrInvalid
	}
	var version string
	_ = json.Unmarshal(fields["plan_version"], &version)
	if version != "v2" {
		return nil
	}
	stats := map[string]map[string]int{}
	if after {
		_ = json.Unmarshal(fields["funnel"], &stats)
		if stats == nil {
			stats = map[string]map[string]int{}
		}
	}
	ageCache := map[int64]string{}
	add := func(stage string, item recommendationFunnelItem) error {
		class, ok := ageCache[item.ArtistID]
		if !ok {
			var err error
			class, err = recommendationArtistAge(ctx, tx, item.ArtistID)
			if err != nil {
				return err
			}
			ageCache[item.ArtistID] = class
		}
		if stats[stage] == nil {
			stats[stage] = map[string]int{}
		}
		stats[stage][item.Source+"/"+class]++
		return nil
	}
	var items []recommendationFunnelItem
	if !after {
		rows, err := tx.QueryContext(ctx, `SELECT artist_id,source FROM recommendation_recall_events WHERE run_id=? ORDER BY artist_id,source,seed_artist_id`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item recommendationFunnelItem
			if err = rows.Scan(&item.ArtistID, &item.Source); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		seen := map[string]bool{}
		for _, item := range items {
			if err = add("recalled", item); err != nil {
				return err
			}
			key := item.Source + ":" + strconv.FormatInt(item.ArtistID, 10)
			if !seen[key] {
				seen[key] = true
				if err = add("deduplicated", item); err != nil {
					return err
				}
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.artist_id,j.value,r.score_version<>'' AND json_array_length(r.sample_illust_ids_json)>0,r.sample_illust_ids_json FROM recommendations r,json_each(r.sources_json) j JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='pending' ORDER BY r.artist_id,j.value`, runID)
	if err != nil {
		return err
	}
	items = nil
	for rows.Next() {
		var item recommendationFunnelItem
		if err = rows.Scan(&item.ArtistID, &item.Source, &item.Valid, &item.Samples); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		if !after && item.Valid {
			ids, ok := decodeRecommendationMixSampleIDs(item.Samples)
			if !ok || len(ids) == 0 {
				item.Valid = false
			} else {
				_, valid, e := readRecommendationMixSamples(ctx, tx, item.ArtistID, ids)
				if e != nil {
					return e
				}
				item.Valid = valid
			}
		}
		if after {
			if err = add("selected", item); err != nil {
				return err
			}
		} else {
			if err = add("admitted", item); err != nil {
				return err
			}
			if item.Valid {
				if err = add("sample_validated", item); err != nil {
					return err
				}
				if err = add("before_quota", item); err != nil {
					return err
				}
			}
		}
	}
	fields["funnel"], _ = json.Marshal(stats)
	if after {
		var targets struct {
			Algorithm int            `json:"algorithm_artists"`
			Ranking   int            `json:"ranking_artists"`
			Uniform   int            `json:"uniform_sample_target"`
			Actual    map[string]int `json:"actual_source_counts"`
		}
		_ = json.Unmarshal([]byte(raw), &targets)
		shortfall := map[string]int{"algorithm": max(0, targets.Algorithm-targets.Actual["algorithm"]), independentRankingSource: max(0, targets.Ranking-targets.Actual[independentRankingSource]), independentUniformSource: max(0, targets.Uniform-targets.Actual[independentUniformSource])}
		fields["source_shortfall"], _ = json.Marshal(shortfall)
		reasons := map[string]string{}
		for source, n := range shortfall {
			if n > 0 {
				reasons[source] = "eligible_candidates_or_content_cap"
			}
		}
		fields["source_shortfall_reasons"], _ = json.Marshal(reasons)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET budget_json=? WHERE id=?`, string(encoded), runID)
	return err
}
