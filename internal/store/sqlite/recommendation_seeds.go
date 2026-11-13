package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"sort"
)

// Freeze diverse seeds at creation; a resumed run must never pick a different set.
func freezeRecommendationSeeds(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (json.RawMessage, error) {
	var b map[string]json.RawMessage
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, domain.ErrInvalid
	}
	var limit int
	if err := json.Unmarshal(b["seed_artists"], &limit); err != nil || limit < 1 {
		return raw, nil
	}
	if limit > 30 {
		return nil, domain.ErrInvalid
	}
	base := ` FROM artists a WHERE (a.review_status='approved' OR a.remote_followed=1) AND a.review_status NOT IN ('blocked','rejected')`
	ids := []int64{}
	pick := func(extra, order string, n int) error {
		if n <= 0 {
			return nil
		}
		args := []any{}
		exclude := ``
		if len(ids) > 0 {
			exclude = ` AND a.id NOT IN (` + placeholders(len(ids)) + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		args = append(args, n)
		rows, err := tx.QueryContext(ctx, `SELECT a.id`+base+extra+exclude+` ORDER BY `+order+` LIMIT ?`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}
	recent := `COALESCE((SELECT MAX(aa.followed_at) FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.followed=1 AND ac.deleted=0),0) DESC,a.created_at DESC,a.id`
	if err := pick("", recent, (limit+1)/2); err != nil {
		return nil, err
	}
	adult := ` AND EXISTS(SELECT 1 FROM artworks w WHERE w.artist_id=a.id AND w.visible=1 AND w.rating IN ('r18','r18g') AND NOT EXISTS(SELECT 1 FROM artwork_preferences p WHERE p.illust_id=w.id AND p.value='dislike'))`
	if err := pick(adult, `random()`, min(limit/4, limit-len(ids))); err != nil {
		return nil, err
	}
	if err := pick("", `random()`, limit-len(ids)); err != nil {
		return nil, err
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	b["seed_artist_ids"] = encoded
	return json.Marshal(b)
}
func (s *Store) RecommendationSeedsForRun(ctx context.Context, runID string, limit int) ([]domain.Artist, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&raw); err != nil {
		return nil, err
	}
	var b struct {
		IDs []int64 `json:"seed_artist_ids"`
	}
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, err
	}
	if b.IDs == nil {
		return s.RecommendationSeeds(ctx, limit)
	}
	if len(b.IDs) == 0 {
		return []domain.Artist{}, nil
	}
	if len(b.IDs) > 30 {
		return nil, domain.ErrInvalid
	}
	args := []any{}
	for _, id := range b.IDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,account,avatar_url,review_status,collection_enabled,remote_followed FROM artists WHERE id IN (`+placeholders(len(b.IDs))+`) AND (review_status='approved' OR remote_followed=1) AND review_status NOT IN ('blocked','rejected') ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Artist{}
	for rows.Next() {
		var a domain.Artist
		if err = rows.Scan(&a.ID, &a.Name, &a.Account, &a.AvatarURL, &a.ReviewStatus, &a.CollectionEnabled, &a.RemoteFollowed); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
