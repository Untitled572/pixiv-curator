package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Selection is persisted inside the completion transaction, so refresh/retry cannot reroll exploration.
func selectRecommendationExploration(ctx context.Context, tx *sql.Tx, runID string) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		return err
	}
	var b struct {
		CandidateArtists   int `json:"candidate_artists"`
		ExplorationArtists int `json:"exploration_artists"`
	}
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return err
	}
	if b.ExplorationArtists <= 0 {
		return nil
	}
	limit := min(30, b.CandidateArtists)
	if limit < 1 {
		return nil
	}
	base := ` FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='pending' AND EXISTS(SELECT 1 FROM json_each(r.sample_illust_ids_json) sample JOIN artworks w ON w.id=CAST(sample.value AS INTEGER) JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 WHERE w.artist_id=a.id AND w.visible=1 AND p.media_state='indexed' AND NOT EXISTS(SELECT 1 FROM artwork_preferences pref WHERE pref.illust_id=w.id AND pref.value='dislike'))`
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+base, runID).Scan(&count); err != nil {
		return err
	}
	target := min(limit, count)
	randomCount := min(b.ExplorationArtists, target/5)
	normalCount := target - randomCount
	read := func(q string, args ...any) ([]int64, error) {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}
	normal, err := read(`SELECT r.artist_id`+base+` ORDER BY r.score DESC,r.artist_id LIMIT ?`, runID, normalCount)
	if err != nil {
		return err
	}
	args := []any{runID}
	exclude := ``
	if len(normal) > 0 {
		exclude = ` AND r.artist_id NOT IN (` + placeholders(len(normal)) + `)`
		for _, id := range normal {
			args = append(args, id)
		}
	}
	args = append(args, randomCount)
	random, err := read(`SELECT r.artist_id`+base+exclude+` ORDER BY random() LIMIT ?`, args...)
	if err != nil {
		return err
	}
	for _, group := range []struct {
		ids  []int64
		flag string
	}{{normal, "false"}, {random, "true"}} {
		for _, id := range group.ids {
			if _, err = tx.ExecContext(ctx, `UPDATE recommendations SET components_json=json_set(components_json,'$.exploration',json(?)) WHERE run_id=? AND artist_id=?`, group.flag, runID, id); err != nil {
				return err
			}
		}
	}
	ids := append(normal, random...)
	if len(ids) == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM recommendations WHERE run_id=?`, runID)
		return err
	}
	args = []any{runID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM recommendations WHERE run_id=? AND artist_id NOT IN (`+placeholders(len(ids))+`)`, args...)
	return err
}
