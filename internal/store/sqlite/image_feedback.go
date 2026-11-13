package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// SetImageFeedback replaces the current preference for one fixed catalog page.
// Neutral removes the override; no event is accumulated.
func (s *Store) SetImageFeedback(ctx context.Context, illustID int64, pageIndex int, value string) (string, error) {
	if illustID <= 0 || pageIndex < 0 || (value != "like" && value != "dislike" && value != "neutral") {
		return "", domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM image_pages WHERE illust_id=? AND page_index=?)`, illustID, pageIndex).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", domain.ErrNotFound
	}
	if value == "neutral" {
		_, err = tx.ExecContext(ctx, `DELETE FROM image_feedback WHERE illust_id=? AND page_index=?`, illustID, pageIndex)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO image_feedback(illust_id,page_index,value,updated_at) VALUES(?,?,?,?) ON CONFLICT(illust_id,page_index) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at WHERE image_feedback.value<>excluded.value`, illustID, pageIndex, value, time.Now().UnixMilli())
	}
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return value, nil
}

// ImageFeedbackTags returns bounded signed per-tag page counts, counting each
// normalized tag once per feedback page.
func (s *Store) ImageFeedbackTags(ctx context.Context) (map[string]float64, error) {
	counts := map[string]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT t.normalized_name,SUM(CASE f.value WHEN 'like' THEN 1.0 ELSE -1.0 END) AS score
		FROM image_feedback f JOIN artwork_tags at ON at.illust_id=f.illust_id JOIN tags t ON t.id=at.tag_id
		GROUP BY t.normalized_name ORDER BY ABS(score) DESC,t.normalized_name LIMIT 10000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		var score float64
		if err = rows.Scan(&tag, &score); err != nil {
			return nil, err
		}
		counts[strings.ToLower(strings.TrimSpace(tag))] = score
	}
	return counts, rows.Err()
}

var _ interface {
	SetImageFeedback(context.Context, int64, int, string) (string, error)
	ImageFeedbackTags(context.Context) (map[string]float64, error)
} = (*Store)(nil)
