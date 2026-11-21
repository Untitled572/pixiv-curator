package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
	"strings"
	"time"
)

type recommendationFeatureSample struct {
	IllustID int64    `json:"illust_id"`
	Rating   string   `json:"rating"`
	Tags     []string `json:"tags"`
}

// recommendationFeatureSnapshot is called before any decision removes works.
func recommendationFeatureSnapshot(ctx context.Context, tx *sql.Tx, artistID int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.rating,COALESCE(group_concat(t.normalized_name,char(31)),'')
 FROM artworks w LEFT JOIN artwork_tags at ON at.illust_id=w.id LEFT JOIN tags t ON t.id=at.tag_id
 WHERE w.artist_id=? AND w.visible=1 AND (EXISTS(SELECT 1 FROM artists a WHERE a.id=w.artist_id AND a.review_status='approved') OR NOT EXISTS(SELECT 1 FROM recommendations r WHERE r.artist_id=w.artist_id) OR EXISTS(SELECT 1 FROM recommendations r, json_each(r.sample_illust_ids_json) j WHERE r.artist_id=w.artist_id AND r.run_id=(SELECT rr.id FROM recommendation_runs rr JOIN recommendations x ON x.run_id=rr.id WHERE x.artist_id=w.artist_id ORDER BY rr.started_at DESC,rr.rowid DESC LIMIT 1) AND CAST(j.value AS INTEGER)=w.id))
 GROUP BY w.id ORDER BY COALESCE(w.bookmarks,0) DESC,COALESCE(w.published_at,0) DESC,w.id DESC LIMIT 3`, artistID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	samples := []recommendationFeatureSample{}
	for rows.Next() {
		var sample recommendationFeatureSample
		var tags string
		if err = rows.Scan(&sample.IllustID, &sample.Rating, &tags); err != nil {
			return "", err
		}
		sample.Tags = []string{}
		if tags != "" {
			sample.Tags = strings.Split(tags, "\x1f")
		}
		samples = append(samples, sample)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(samples)
	return string(raw), err
}

// recordRecommendationDecision freezes evidence and clears all prior weak skips.
// Call inside the decision transaction before catalog cleanup.
func recordRecommendationDecision(ctx context.Context, tx *sql.Tx, artistID int64, action string) error {
	if action != "approve" && action != "reject" {
		return nil
	}
	snapshot, err := recommendationFeatureSnapshot(ctx, tx, artistID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recommendation_decisions(artist_id,action,feature_json,decided_at) VALUES(?,?,?,?)`, artistID, action, snapshot, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM recommendation_weak_skips WHERE artist_id=?`, artistID)
	return err
}

func validateRecommendationExposure(ctx context.Context, tx *sql.Tx, budget []byte) error {
	var b struct {
		Parent string  `json:"exposure_parent"`
		IDs    []int64 `json:"exposure_ids"`
	}
	if json.Unmarshal(budget, &b) != nil || len(b.IDs) > 30 {
		return domain.ErrInvalid
	}
	if b.Parent == "" {
		if len(b.IDs) > 0 {
			return domain.ErrInvalid
		}
		return nil
	}
	var latest string
	err := tx.QueryRowContext(ctx, `SELECT id FROM recommendation_runs WHERE state='succeeded' ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&latest)
	if err != nil || latest != b.Parent {
		return domain.ErrConflict
	}
	var directed bool
	if err = tx.QueryRowContext(ctx, `SELECT json_type(budget_json,'$.directed') IS NOT NULL FROM recommendation_runs WHERE id=?`, b.Parent).Scan(&directed); err != nil {
		return err
	}
	if directed && len(b.IDs) > 0 {
		return domain.ErrInvalid
	}
	seen := map[int64]bool{}
	for _, id := range b.IDs {
		if id <= 0 || seen[id] {
			return domain.ErrInvalid
		}
		seen[id] = true
		var eligible bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND r.artist_id=? AND a.review_status='pending')`, b.Parent, id).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return domain.ErrConflict
		}
	}
	return nil
}

func settleRecommendationExposure(ctx context.Context, tx *sql.Tx, runID string) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		return err
	}
	var b struct {
		Parent      string  `json:"exposure_parent"`
		IDs         []int64 `json:"exposure_ids"`
		WeakEnabled bool    `json:"weak_feedback_enabled"`
	}
	if json.Unmarshal([]byte(raw), &b) != nil {
		return domain.ErrInvalid
	}
	if !b.WeakEnabled || b.Parent == "" || len(b.IDs) == 0 {
		return nil
	}
	// A different successful run has already replaced this parent.
	var latest string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recommendation_runs WHERE state='succeeded' ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&latest); err != nil {
		return err
	}
	if latest != b.Parent {
		return nil
	}
	for _, id := range b.IDs {
		var eligible bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND r.artist_id=? AND a.review_status='pending')`, b.Parent, id).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			continue
		}
		snapshot, err := recommendationFeatureSnapshot(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO recommendation_weak_skips(run_id,artist_id,feature_json,skipped_at) VALUES(?,?,?,?)`, b.Parent, id, snapshot, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func recommendationSkipWeight(ctx context.Context, tx *sql.Tx, artistID int64, now time.Time) (float64, error) {
	const days = 45.0
	cutoff := now.Add(-45 * 24 * time.Hour).UnixMilli()
	var effective float64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(1.0-(?-skipped_at)/?),0) FROM recommendation_weak_skips WHERE artist_id=? AND skipped_at>? AND skipped_at<=?`, now.UnixMilli(), days*24*60*60*1000, artistID, cutoff, now.UnixMilli()).Scan(&effective)
	if err != nil {
		return 0, err
	}
	return math.Max(0.25, math.Pow(0.70, effective)), nil
}

func (s *Store) RecommendationSimilarityExamples(ctx context.Context, runID string) ([]recommend.SimilarityExample, error) {
	var recentRaw string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(budget_json,'$.recent_approved_artist_ids'),'[]') FROM recommendation_runs WHERE id=?`, runID).Scan(&recentRaw); err != nil {
		return nil, err
	}
	positiveRows, err := s.db.QueryContext(ctx, `WITH ranked AS (
 SELECT w.id,w.artist_id,row_number() OVER(PARTITION BY w.artist_id ORDER BY COALESCE(w.bookmarks,0) DESC,COALESCE(w.published_at,0) DESC,w.id DESC) popular,
 row_number() OVER(PARTITION BY w.artist_id ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC) recent
 FROM artworks w JOIN artists a ON a.id=w.artist_id WHERE w.visible=1 AND w.metadata_state<>'unavailable' AND (a.review_status='approved' OR (a.review_status='pending' AND a.remote_followed=1)) AND NOT EXISTS(SELECT 1 FROM recommendation_decisions d WHERE d.artist_id=a.id AND d.action='approve' AND d.id=(SELECT MAX(x.id) FROM recommendation_decisions x WHERE x.artist_id=a.id))
 ), chosen AS (SELECT id,artist_id FROM ranked WHERE popular<=2 OR recent=1)
 SELECT c.artist_id,json_group_array(DISTINCT t.normalized_name),CASE WHEN c.artist_id IN (SELECT CAST(value AS INTEGER) FROM json_each(?)) THEN 0.2 ELSE 1 END FROM chosen c LEFT JOIN artwork_tags at ON at.illust_id=c.id LEFT JOIN tags t ON t.id=at.tag_id GROUP BY c.artist_id ORDER BY c.artist_id`, recentRaw)
	if err != nil {
		return nil, err
	}
	out := []recommend.SimilarityExample{}
	for positiveRows.Next() {
		var id int64
		var raw string
		var weight float64
		if err = positiveRows.Scan(&id, &raw, &weight); err != nil {
			positiveRows.Close()
			return nil, err
		}
		var tags []string
		if err = json.Unmarshal([]byte(raw), &tags); err != nil {
			positiveRows.Close()
			return nil, err
		}
		if len(tags) > 0 {
			out = append(out, recommend.SimilarityExample{Tags: tags, Weight: weight})
		}
	}
	if err = positiveRows.Err(); err != nil {
		positiveRows.Close()
		return nil, err
	}
	positiveRows.Close()
	now := time.Now()
	cutoff := now.Add(-45 * 24 * time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT d.feature_json,d.action='reject',0,CASE WHEN d.artist_id IN (SELECT CAST(value AS INTEGER) FROM json_each(?)) AND d.action='approve' THEN 0.2 ELSE 1.0 END FROM recommendation_decisions d JOIN artists a ON a.id=d.artist_id WHERE d.id=(SELECT MAX(x.id) FROM recommendation_decisions x WHERE x.artist_id=d.artist_id) AND ((d.action='reject' AND a.review_status='rejected') OR (d.action='approve' AND a.review_status='approved'))
 UNION ALL
 SELECT w.feature_json,0,1,MAX(0.0,1.0-(?-w.skipped_at)/?) FROM recommendation_weak_skips w JOIN artists a ON a.id=w.artist_id WHERE a.review_status='pending' AND w.skipped_at>? AND w.rowid IN (SELECT MAX(x.rowid) FROM recommendation_weak_skips x WHERE x.skipped_at>? GROUP BY x.artist_id ORDER BY MAX(x.skipped_at) DESC LIMIT 150)`, recentRaw, now.UnixMilli(), float64(45*24*time.Hour/time.Millisecond), cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var negative, weak bool
		var weight float64
		if err = rows.Scan(&raw, &negative, &weak, &weight); err != nil {
			return nil, err
		}
		var samples []recommendationFeatureSample
		if err = json.Unmarshal([]byte(raw), &samples); err != nil {
			return nil, err
		}
		tags := []string{}
		for _, sample := range samples {
			tags = append(tags, sample.Tags...)
		}
		out = append(out, recommend.SimilarityExample{Tags: tags, Negative: negative, Weak: weak, Weight: weight})
	}
	return out, rows.Err()
}
func (s *Store) RecommendationSeedHits(ctx context.Context, runID string, artistID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM recommendation_seed_hits WHERE run_id=? AND artist_id=?`, runID, artistID).Scan(&count)
	return count, err
}
