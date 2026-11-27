package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const (
	historyCollection       = "preferred"
	distributionHistoryAge  = 30 * 24 * time.Hour
	distributionHistoryKeep = 10000
)

// History reads retained selection records without changing or filtering them
// by current public preferences. Availability reflects whether the fixed page
// currently satisfies the public image endpoint's eligibility rules.
func (s *Store) History(ctx context.Context, page, pageSize int) (domain.DistributionHistoryPage, error) {
	out := domain.DistributionHistoryPage{Items: []domain.DistributionHistoryItem{}, Page: page, PageSize: pageSize}
	if page < 1 || pageSize < 1 || pageSize > 96 {
		return out, domain.ErrInvalid
	}

	tx, err := s.adminReadDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM distribution_history h JOIN artworks a ON a.id=h.illust_id WHERE h.collection=? AND a.kind<>'manga'`, historyCollection).Scan(&out.Total); err != nil {
		return out, err
	}
	if out.Total == 0 {
		return out, tx.Commit()
	}
	out.TotalPages = (out.Total-1)/int64(pageSize) + 1
	if int64(page) > out.TotalPages {
		return out, tx.Commit()
	}

	offset := int64(page-1) * int64(pageSize)
	query := `SELECT h.id,h.selected_at,h.illust_id,h.page_index,MAX(a.page_count,h.page_index+1),a.artist_id,ar.name,a.title,p.width,p.height,
		CASE WHEN a.visible=1 AND ar.review_status<>'blocked' AND p.media_state='indexed' THEN 1 ELSE 0 END,p.original_url,a.rating,a.ai_status,COALESCE((SELECT json_group_array(t.display_name) FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id=a.id),'[]')
		,COALESCE(f.value,'neutral'),h.delivered_at,COALESCE(h.delivery_mode,''),h.filter_preset,
		CASE WHEN a.retained_page_indices IS NOT NULL THEN
			(SELECT json_group_array(page_index ORDER BY page_index) FROM image_pages
			 WHERE illust_id=a.id AND media_state='indexed') END
		FROM distribution_history h
		JOIN artworks a ON a.id=h.illust_id
		JOIN artists ar ON ar.id=a.artist_id
		JOIN image_pages p ON p.illust_id=h.illust_id AND p.page_index=h.page_index
		LEFT JOIN image_feedback f ON f.illust_id=h.illust_id AND f.page_index=h.page_index
		WHERE h.collection=? AND a.kind<>'manga'
		ORDER BY h.selected_at DESC,h.id DESC LIMIT ? OFFSET ?`
	args := []any{historyCollection, pageSize, offset}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item domain.DistributionHistoryItem
		var available int
		var originalURL, rawTags string
		var rawPreset []byte
		var retainedPages sql.NullString
		if err = rows.Scan(&item.ID, &item.SelectedAt, &item.IllustID, &item.PageIndex, &item.PageCount, &item.ArtistID,
			&item.ArtistName, &item.Title, &item.Width, &item.Height, &available, &originalURL, &item.Rating, &item.AIStatus, &rawTags, &item.Feedback, &item.DeliveredAt, &item.DeliveryMode, &rawPreset, &retainedPages); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(rawTags), &item.Tags); err != nil {
			return out, err
		}
		if retainedPages.Valid {
			if err = json.Unmarshal([]byte(retainedPages.String), &item.PageIndices); err != nil {
				return out, err
			}
		}
		if json.Valid(rawPreset) {
			item.FilterPreset = rawPreset
		}
		item.Available = available == 1 && domain.ValidImageURL(originalURL)
		item.ThumbnailURL = fmt.Sprintf("/admin/api/previews/%d/%d", item.IllustID, item.PageIndex)
		item.FullURL = fmt.Sprintf("/admin/api/previews/%d/%d?size=original", item.IllustID, item.PageIndex)
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

type historyExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func trimDistributionHistory(ctx context.Context, exec historyExecer, now time.Time) error {
	if _, err := exec.ExecContext(ctx, `DELETE FROM distribution_history WHERE selected_at<?`, now.Add(-distributionHistoryAge).UnixMilli()); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `DELETE FROM distribution_history WHERE id NOT IN (
		SELECT id FROM distribution_history ORDER BY selected_at DESC,id DESC LIMIT ?
	)`, distributionHistoryKeep)
	return err
}

var _ interface {
	History(context.Context, int, int) (domain.DistributionHistoryPage, error)
} = (*Store)(nil)
