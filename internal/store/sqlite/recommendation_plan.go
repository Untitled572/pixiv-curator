package sqlite

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const independentSource = "independent_ranking"

func freezeExplorationPlan(raw json.RawMessage) (json.RawMessage, error) {
	var b map[string]json.RawMessage
	if json.Unmarshal(raw, &b) != nil || b == nil {
		return nil, domain.ErrInvalid
	}
	var enabled bool
	_ = json.Unmarshal(b["independent_exploration"], &enabled)
	if !enabled {
		return raw, nil
	}
	var candidates int
	if err := json.Unmarshal(b["candidate_artists"], &candidates); err != nil || candidates < 0 || candidates > 60 {
		return nil, domain.ErrInvalid
	}
	rankingTarget := int(float64(candidates)*10/30 + 0.5)
	uniformTarget := int(float64(candidates)*3/30 + 0.5)
	algorithmTarget := candidates - rankingTarget - uniformTarget
	explorationTarget := rankingTarget + uniformTarget
	set := func(key string, value any) error {
		encoded, err := json.Marshal(value)
		if err == nil {
			b[key] = encoded
		}
		return err
	}
	if err := set("algorithm_artists", algorithmTarget); err != nil {
		return nil, err
	}
	if err := set("ranking_artists", rankingTarget); err != nil {
		return nil, err
	}
	if err := set("uniform_sample_target", uniformTarget); err != nil {
		return nil, err
	}
	if err := set("exploration_artists", explorationTarget); err != nil {
		return nil, err
	}
	independentLimit := 2 * explorationTarget
	if err := set("independent_candidate_limit", independentLimit); err != nil {
		return nil, err
	}
	if err := set("ranking_candidate_limit", 2*rankingTarget); err != nil {
		return nil, err
	}
	if err := set("uniform_candidate_limit", 2*uniformTarget); err != nil {
		return nil, err
	}
	if err := set("exploration_cursor", 0); err != nil {
		return nil, err
	}
	if err := set("uniform_cursor", 0); err != nil {
		return nil, err
	}
	if err := set("ranking_max_illust_id", int64(0)); err != nil {
		return nil, err
	}
	if err := set("uniform_probe_ids", []int64{}); err != nil {
		return nil, err
	}
	type query struct {
		Mode   string `json:"mode"`
		Date   string `json:"date"`
		Offset int    `json:"offset"`
		Seed   int64  `json:"shuffle_seed"`
	}
	queries := []query{}
	if explorationTarget > 0 {
		seed, err := randomExplorationInt(1 << 62)
		if err != nil {
			return nil, err
		}
		queries = append(queries, query{Mode: "day", Date: time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02"), Offset: 0, Seed: seed + 1})
		modes := []string{"day", "week", "month", "day_male", "day_female", "week_original", "week_rookie", "day_r18", "week_r18"}
		for i := 0; i < 3; i++ {
			m, err := randomExplorationInt(int64(len(modes)))
			if err != nil {
				return nil, err
			}
			days, err := randomExplorationInt(1094)
			if err != nil {
				return nil, err
			}
			offset, err := randomExplorationInt(10)
			if err != nil {
				return nil, err
			}
			querySeed, err := randomExplorationInt(1 << 62)
			if err != nil {
				return nil, err
			}
			queries = append(queries, query{Mode: modes[m], Date: time.Now().UTC().AddDate(0, 0, -2-int(days)).Format("2006-01-02"), Offset: int(offset) * 30, Seed: querySeed + 1})
		}
	}
	b["exploration_queries"], _ = json.Marshal(queries)
	return json.Marshal(b)
}

func randomExplorationInt(max int64) (int64, error) {
	if max <= 0 {
		return 0, domain.ErrInvalid
	}
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}

// AdvanceExploration persists a completed page, fenced by the current worker lease.
// A crash between recall and this checkpoint may repeat one bounded read.
func (s *Store) AdvanceExploration(ctx context.Context, j domain.Job, runID string, rankingNext, uniformNext int, rankingMax int64) error {
	if rankingNext < -1 || uniformNext < -1 || rankingMax < 0 {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRecommendationLease(ctx, tx, j, runID); err != nil {
		return err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return domain.ErrInvalid
	}
	var currentRanking, currentUniform int
	var currentMax int64
	_ = json.Unmarshal(fields["exploration_cursor"], &currentRanking)
	_ = json.Unmarshal(fields["uniform_cursor"], &currentUniform)
	_ = json.Unmarshal(fields["ranking_max_illust_id"], &currentMax)
	if rankingNext >= 0 && rankingNext > currentRanking {
		currentRanking = rankingNext
	}
	if uniformNext >= 0 && uniformNext > currentUniform {
		currentUniform = uniformNext
	}
	fields["exploration_cursor"], _ = json.Marshal(currentRanking)
	fields["uniform_cursor"], _ = json.Marshal(currentUniform)
	if rankingMax > currentMax {
		currentMax = rankingMax
		fields["ranking_max_illust_id"], _ = json.Marshal(currentMax)
		var target int
		_ = json.Unmarshal(fields["uniform_sample_target"], &target)
		var existing []int64
		_ = json.Unmarshal(fields["uniform_probe_ids"], &existing)
		if target > 0 && len(existing) == 0 {
			probes, probeErr := randomExplorationProbeIDs(currentMax, 18)
			if probeErr != nil {
				return probeErr
			}
			fields["uniform_probe_ids"], _ = json.Marshal(probes)
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET budget_json=? WHERE id=?`, string(encoded), runID); err != nil {
		return err
	}
	return tx.Commit()
}

func randomExplorationProbeIDs(maxID int64, limit int) ([]int64, error) {
	if maxID <= 0 || limit <= 0 {
		return []int64{}, nil
	}
	k := int64(limit)
	if maxID < k {
		k = maxID
	}
	chosen := make(map[int64]bool, int(k))
	for j := maxID - k; j < maxID; j++ {
		t, err := randomExplorationInt(j + 1)
		if err != nil {
			return nil, err
		}
		if chosen[t] {
			chosen[j] = true
		} else {
			chosen[t] = true
		}
	}
	ids := make([]int64, 0, len(chosen))
	for zeroBased := range chosen {
		ids = append(ids, zeroBased+1)
	}
	for i := len(ids) - 1; i > 0; i-- {
		j, err := randomExplorationInt(int64(i + 1))
		if err != nil {
			return nil, err
		}
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids, nil
}
