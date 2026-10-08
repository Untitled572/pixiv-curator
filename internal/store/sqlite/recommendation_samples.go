package sqlite

import (
	"context"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// RecommendationSamples applies explicit feedback before forming bounded samples.
func (s *Store) RecommendationSamples(ctx context.Context, id int64, limit int) ([]domain.Artwork, error) {
	if id <= 0 || limit < 1 || limit > 8 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM artworks a LEFT JOIN artwork_preferences p ON p.illust_id=a.id WHERE a.artist_id=? AND COALESCE(p.value,'neutral')<>'dislike' ORDER BY CASE WHEN p.value='like' THEN 0 ELSE 1 END,a.id LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	samples := []domain.Artwork{}
	for _, workID := range ids {
		works, err := s.Artworks(ctx, workID-1, 1, id)
		if err != nil {
			return nil, err
		}
		if len(works) == 1 && works[0].ID == workID {
			samples = append(samples, works[0])
		}
	}
	return samples, nil
}
func (s *Store) NegativeTags(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.normalized_name,count(*) FROM artwork_preferences p JOIN artwork_tags at ON at.illust_id=p.illust_id JOIN tags t ON t.id=at.tag_id WHERE p.value='dislike' GROUP BY t.normalized_name ORDER BY count(*) DESC,t.normalized_name LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]float64{}
	for rows.Next() {
		var tag string
		var count float64
		if err = rows.Scan(&tag, &count); err != nil {
			return nil, err
		}
		result[tag] = count
	}
	return result, rows.Err()
}
func (s *Store) SaveRecommendationProfile(ctx context.Context, j domain.Job, runID string, profile json.RawMessage) error {
	if !json.Valid(profile) {
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
	_, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET stats_json=? WHERE id=?`, string(profile), runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RecommendationProfile(ctx context.Context, runID string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT stats_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw)
	return json.RawMessage(raw), err
}
