package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"time"
)

func (s *Store) selectStratifiedShort(ctx context.Context, req domain.SelectionRequest) (domain.SelectedPage, error) {
	for attempt := 0; attempt < 8; attempt++ {
		r, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if e != nil {
			return domain.SelectedPage{}, e
		}
		var exists bool
		e = r.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks)`).Scan(&exists)
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		if !exists {
			r.Rollback()
			return domain.SelectedPage{}, domain.ErrCatalogEmpty
		}
		g, e := preferencesInTx(ctx, r)
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		rules, no, e := combineSelectionRules(g, req)
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		if no {
			r.Rollback()
			return domain.SelectedPage{}, domain.ErrNoMatch
		}
		recent, e := recentArtworkIDs(ctx, r, time.Now().Add(-selectionWindow).UnixMilli())
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		pick, e := s.choosePage(ctx, r, rules, req.ArtistID, req.AccountID, recent, false)
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		repeat := false
		if pick.page.IllustID == 0 {
			pick, e = s.choosePage(ctx, r, rules, req.ArtistID, req.AccountID, nil, true)
			repeat = pick.page.IllustID != 0
		}
		if e != nil {
			r.Rollback()
			return domain.SelectedPage{}, e
		}
		if pick.page.IllustID == 0 {
			r.Rollback()
			return domain.SelectedPage{}, domain.ErrNoMatch
		}
		if e = r.Commit(); e != nil {
			return domain.SelectedPage{}, e
		}
		id, e := newSelectionID()
		if e != nil {
			return domain.SelectedPage{}, e
		}
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return domain.SelectedPage{}, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE settings SET value_json=value_json WHERE 0`); e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		g, e = preferencesInTx(ctx, tx)
		if e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		current, no, e := combineSelectionRules(g, req)
		if e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		a, _ := json.Marshal(rules)
		b, _ := json.Marshal(current)
		if no || string(a) != string(b) || rules.DeliveryMode != current.DeliveryMode {
			tx.Rollback()
			continue
		}
		recent, e = recentArtworkIDs(ctx, tx, time.Now().Add(-selectionWindow).UnixMilli())
		if e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		excluded := recent
		if repeat {
			excluded = nil
		}
		where, args, e := selectionWhere(current, req.ArtistID, req.AccountID, excluded)
		if e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		where = appendAnd(where, `a.id=? AND p.page_index=?`)
		args = append(args, pick.page.IllustID, pick.page.PageIndex)
		var p domain.SelectedPage
		e = tx.QueryRowContext(ctx, selectionPageQuery(where, ""), args...).Scan(&p.IllustID, &p.ArtistID, &p.PageIndex, &p.Width, &p.Height, &p.OriginalURL, &p.PreviewURL)
		if e == sql.ErrNoRows {
			tx.Rollback()
			continue
		}
		if e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		if !domain.ValidImageURL(p.OriginalURL) {
			tx.Rollback()
			continue
		}
		if repeat {
			where, args, e = selectionWhere(current, req.ArtistID, req.AccountID, recent)
			if e != nil {
				tx.Rollback()
				return domain.SelectedPage{}, e
			}
			var available bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(`+selectionPageQuery(where, "")+`)`, args...).Scan(&available)
			if e != nil {
				tx.Rollback()
				return domain.SelectedPage{}, e
			}
			if available {
				tx.Rollback()
				continue
			}
		}
		now := time.Now().UnixMilli()
		p.SelectionID = id
		p.Repeated = repeat
		if _, e = tx.ExecContext(ctx, `INSERT INTO selection_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,?,?,?,?,?)`, id, selectionCollection, p.IllustID, p.PageIndex, now, id); e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		if e = insertDistributionSelection(ctx, tx, p, current, req, now, id); e != nil {
			tx.Rollback()
			return domain.SelectedPage{}, e
		}
		if e = tx.Commit(); e != nil {
			return domain.SelectedPage{}, e
		}
		s.trimNewSelection(ctx, now)
		return p, nil
	}
	return domain.SelectedPage{}, domain.ErrNoMatch
}
func (s *Store) trimNewSelection(ctx context.Context, now int64) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if trimDistributionHistory(ctx, tx, time.UnixMilli(now)) != nil {
		return
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM selection_history WHERE collection=? AND selected_at<?`, selectionCollection, now-selectionWindow.Milliseconds()); e != nil {
		return
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM selection_history WHERE collection=? AND id NOT IN (SELECT id FROM selection_history WHERE collection=? ORDER BY selected_at DESC,id DESC LIMIT ?)`, selectionCollection, selectionCollection, selectionRecentMax); e != nil {
		return
	}
	_ = tx.Commit()
}
