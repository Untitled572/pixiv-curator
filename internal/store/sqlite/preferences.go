package sqlite

import (
	"context"
	"database/sql"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"time"
)

func (s *Store) SetArtworkPreference(ctx context.Context, id int64, value string) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	switch value {
	case "like", "dislike", "neutral":
	default:
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM artworks WHERE id=?`, id).Scan(&found); err == sql.ErrNoRows {
		return domain.ErrNotFound
	} else if err != nil {
		return err
	}
	old := "neutral"
	if err = tx.QueryRowContext(ctx, `SELECT value FROM artwork_preferences WHERE illust_id=?`, id).Scan(&old); err != nil && err != sql.ErrNoRows {
		return err
	}
	if old == value {
		return tx.Commit()
	}
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO artwork_preferences VALUES(?,?,?) ON CONFLICT(illust_id) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, id, value, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feedback_events(id,illust_id,action,source,created_at) VALUES(?,?,?,'admin',?)`, randomID(), id, value, now); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CleanupSelectionHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM selection_history WHERE selected_at<?`, time.Now().Add(-30*time.Minute).UnixMilli())
	return err
}
