package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func insertDistributionSelection(ctx context.Context, tx *sql.Tx, page domain.SelectedPage, rules domain.SelectionRules, req domain.SelectionRequest, now int64, id string) error {
	preset, err := json.Marshal(struct {
		domain.SelectionRules
		ArtistID  int64  `json:"user_id,string,omitempty"`
		AccountID string `json:"account_id,omitempty"`
	}{rules, req.ArtistID, req.AccountID})
	if err != nil {
		return err
	}
	mode := "proxy"
	if rules.DeliveryMode == "original" {
		mode = "original"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id,filter_preset,delivery_mode)
		VALUES(?,?,?,?,?,?,?,?)`, id, selectionCollection, page.IllustID, page.PageIndex, now, id, string(preset), mode)
	if err != nil || mode != "original" {
		return err
	}
	return recordExposure(ctx, tx, page.IllustID, page.ArtistID, normalizedOrientation(rules.Orientation), now)
}

func recordExposure(ctx context.Context, tx *sql.Tx, workID, artistID int64, orientation string, at int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO exposure_work(illust_id,exposed_at) VALUES(?,?)
		ON CONFLICT(illust_id) DO UPDATE SET exposed_at=MAX(exposed_at,excluded.exposed_at)`, workID, at); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO exposure_artist(artist_id,orientation,exposed_at) VALUES(?,?,?)
		ON CONFLICT(artist_id,orientation) DO UPDATE SET exposed_at=MAX(exposed_at,excluded.exposed_at)`, artistID, orientation, at)
	return err
}

// RecordImageTransfers preserves measured dimensions and associates confirmed proxy
// delivery with the selected image identity. Stale transfers cannot recreate rows.
func (s *Store) RecordImageTransfers(ctx context.Context, events []domain.ImageTransfer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, event := range events {
		if event.Width > 0 && event.Height > 0 && domain.ValidImageURL(event.OriginalURL) {
			if _, err = tx.ExecContext(ctx, `UPDATE image_pages SET width=?,height=?
				WHERE illust_id=? AND page_index=? AND original_url=? AND (width=0 OR height=0)`,
				event.Width, event.Height, event.IllustID, event.PageIndex, event.OriginalURL); err != nil {
				return err
			}
		}
		if event.SelectionID == "" || event.At <= 0 {
			continue
		}
		var artistID int64
		var orientation string
		err = tx.QueryRowContext(ctx, `UPDATE distribution_history SET delivered_at=?
			WHERE id=? AND illust_id=? AND page_index=? AND delivery_mode='proxy' AND delivered_at IS NULL AND selected_at<=?
			RETURNING (SELECT artist_id FROM artworks WHERE id=?),COALESCE(json_extract(filter_preset,'$.orientation'),'any')`,
			event.At, event.SelectionID, event.IllustID, event.PageIndex, event.At, event.IllustID).Scan(&artistID, &orientation)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if err = recordExposure(ctx, tx, event.IllustID, artistID, normalizedOrientation(orientation), event.At); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, event := range events {
		if event.Width > 0 && event.Height > 0 {
			s.RequestSelectionSnapshotRefresh()
			break
		}
	}
	return nil
}
