package sqlite

import (
	"context"
	"database/sql"
	"math"
	rand "math/rand/v2"
	"time"
)

type cleanupCandidate struct {
	id       int64
	priority bool
	reason   string
	weight   float64
	last     int64
	aiID     sql.NullInt64
	pending  bool
}

func fillCleanupBatch(ctx context.Context, tx *sql.Tx, round int64) error {
	now := time.Now().UnixMilli()
	day := int64((24 * time.Hour) / time.Millisecond)
	// Start the initial grace period only once. Older installations retain unknown enable times.
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO library_cleanup_skip(artist_id,until_round,started_at,min_at,max_at) SELECT id,0,cleanup_enabled_at,cleanup_enabled_at+?,cleanup_enabled_at+? FROM artists WHERE review_status='approved' AND collection_enabled=1 AND cleanup_enabled_at IS NOT NULL`, 3*day, 3*day)
	if err != nil {
		return err
	}
	// Latch new evidence until the artist actually enters a batch.
	_, err = tx.ExecContext(ctx, `UPDATE library_cleanup_skip SET pending_reason=CASE WHEN (SELECT COUNT(*) FROM image_feedback f JOIN artworks w ON w.id=f.illust_id WHERE w.artist_id=library_cleanup_skip.artist_id AND f.value='dislike' AND f.updated_at>library_cleanup_skip.started_at)>=2 THEN 'new_dislikes' ELSE 'new_ai' END, pending_ai_id=(SELECT w.id FROM artworks w WHERE w.artist_id=library_cleanup_skip.artist_id AND w.visible=1 AND w.ai_status='ai' AND w.created_at>library_cleanup_skip.started_at ORDER BY w.created_at DESC,w.id DESC LIMIT 1) WHERE started_at IS NOT NULL AND pending_reason='' AND (EXISTS(SELECT 1 FROM artworks w WHERE w.artist_id=library_cleanup_skip.artist_id AND w.visible=1 AND w.ai_status='ai' AND w.created_at>library_cleanup_skip.started_at) OR (SELECT COUNT(*) FROM image_feedback f JOIN artworks w ON w.id=f.illust_id WHERE w.artist_id=library_cleanup_skip.artist_id AND f.value='dislike' AND f.updated_at>library_cleanup_skip.started_at)>=2)`)
	if err != nil {
		return err
	}
	rules, err := defaultSelectionRulesInTx(ctx, tx)
	if err != nil {
		return err
	}
	where, args, err := selectionWhere(rules, 0, "", nil)
	if err != nil {
		return err
	}
	queryArgs := []any{now, round, now, round, now - 30*day, now - 30*day, now - 30*day, now - 30*day}
	queryArgs = append(queryArgs, args...)
	rows, err := tx.QueryContext(ctx, `WITH candidates AS MATERIALIZED (
      SELECT ar.id,ar.cleanup_enabled_at,ar.last_synced_at,COALESCE(s.last_batch_round,0) AS last_batch_round,
        COALESCE(s.pending_reason,'') AS pending_reason,s.pending_ai_id
      FROM artists ar LEFT JOIN library_cleanup_skip s ON s.artist_id=ar.id
      WHERE ar.review_status='approved' AND ar.collection_enabled=1 AND (
        s.pending_reason<>'' OR
        (s.started_at IS NOT NULL AND (s.max_at<=? OR (s.until_round<=? AND s.min_at<=?))) OR
        (s.started_at IS NULL AND COALESCE(s.until_round,0)<?)
      )
    ), artwork_stats AS (
      SELECT w.artist_id,COUNT(*) AS total,SUM(kind='manga' AND published_at>=?) AS recent_manga,SUM(published_at>=?) AS recent_works,SUM(ai_status='ai' AND published_at>=?) AS recent_ai FROM candidates c CROSS JOIN artworks w INDEXED BY artworks_artist ON w.artist_id=c.id WHERE w.visible=1 GROUP BY w.artist_id
    ), page_stats AS (
      SELECT w.artist_id,COUNT(*) AS pages,SUM(p.page_index=0) AS previews FROM candidates c CROSS JOIN artworks w INDEXED BY artworks_artist ON w.artist_id=c.id JOIN image_pages p ON p.illust_id=w.id WHERE w.visible=1 AND p.media_state='indexed' GROUP BY w.artist_id
    ), feedback_stats AS (
      SELECT w.artist_id,SUM(f.value='like') AS likes,SUM(f.value='dislike') AS dislikes,SUM(f.value='dislike' AND f.updated_at>=?) AS recent_dislikes FROM artworks w JOIN image_feedback f ON f.illust_id=w.id GROUP BY w.artist_id
    ), eligible_stats AS (
      SELECT a.artist_id,COUNT(DISTINCT a.id) AS qualified FROM candidates c CROSS JOIN artworks a INDEXED BY artworks_artist ON a.artist_id=c.id JOIN artists ar ON ar.id=c.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where+` GROUP BY a.artist_id
    ) SELECT c.id,c.cleanup_enabled_at,c.last_synced_at,c.last_batch_round,c.pending_reason,c.pending_ai_id,
      COALESCE(ast.total,0),COALESCE(ast.recent_manga,0),COALESCE(ast.recent_works,0),COALESCE(ast.recent_ai,0),COALESCE(ps.pages,0),COALESCE(ps.previews,0),COALESCE(fs.likes,0),COALESCE(fs.dislikes,0),COALESCE(fs.recent_dislikes,0),COALESCE(es.qualified,0)
      FROM candidates c LEFT JOIN artwork_stats ast ON ast.artist_id=c.id LEFT JOIN page_stats ps ON ps.artist_id=c.id LEFT JOIN feedback_stats fs ON fs.artist_id=c.id LEFT JOIN eligible_stats es ON es.artist_id=c.id`, queryArgs...)
	if err != nil {
		return err
	}
	type base struct {
		id                                                                                                     int64
		enabled, synced, ai                                                                                    sql.NullInt64
		last                                                                                                   int64
		pending                                                                                                string
		total, recentManga, recentWorks, recentAI, pages, previews, likes, dislikes, recentDislikes, qualified int64
	}
	bases := []base{}
	for rows.Next() {
		var b base
		if err = rows.Scan(&b.id, &b.enabled, &b.synced, &b.last, &b.pending, &b.ai, &b.total, &b.recentManga, &b.recentWorks, &b.recentAI, &b.pages, &b.previews, &b.likes, &b.dislikes, &b.recentDislikes, &b.qualified); err != nil {
			rows.Close()
			return err
		}
		bases = append(bases, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	candidates := []cleanupCandidate{}
	for _, b := range bases {
		total, pages, previews, likes, dislikes, recentDislikes, recentWorks, recentManga, recentAI, qualified := b.total, b.pages, b.previews, b.likes, b.dislikes, b.recentDislikes, b.recentWorks, b.recentManga, b.recentAI, b.qualified
		if b.enabled.Valid && !(b.synced.Valid && b.synced.Int64 >= b.enabled.Int64) && previews < 3 {
			continue
		}
		c := cleanupCandidate{id: b.id, last: b.last, weight: 1, aiID: b.ai, pending: b.pending != ""}
		net := dislikes - likes
		if net > 0 {
			c.priority = true
			c.reason = "dislikes"
			c.weight += math.Min(8, float64(net)*3)
		}
		c.weight += math.Min(4, .5*math.Log1p(float64(pages)))
		if pages >= 1000 {
			c.priority = true
			if c.reason == "" {
				c.reason = "large_catalog"
			}
		}
		if total >= 10 && float64(qualified)/float64(total) < .1 {
			c.priority = true
			c.weight += 2.5
			if c.reason == "" {
				c.reason = "low_availability"
			}
		}
		if recentAI > 0 || b.pending == "new_ai" || (b.pending != "" && b.ai.Valid) {
			c.priority = true
			c.weight += 2.5
			if c.reason == "" {
				c.reason = "recent_ai"
			}
		} else if recentWorks >= 5 && float64(recentManga)/float64(recentWorks) >= .8 {
			c.priority = true
			c.weight += 1.5
			if c.reason == "" {
				c.reason = "recent_manga"
			}
		}
		if b.enabled.Valid && now-b.enabled.Int64 >= 30*day && previews == 0 {
			c.priority = true
			c.weight += 4
			if c.reason == "" {
				c.reason = "no_preview"
			}
		}
		if b.pending != "" {
			c.priority = true
			c.reason = b.pending
			if b.pending == "new_dislikes" && net <= 0 {
				c.weight += 3
			}
		}
		if likes-dislikes >= 3 && recentDislikes == 0 {
			c.weight *= .3
		}
		candidates = append(candidates, c)
	}
	// Weighted sampling without replacement, capped at four priority slots.
	choose := func(priority bool) int {
		total := 0.0
		for _, c := range candidates {
			if (priority && c.priority) || (!priority && !c.pending) {
				if priority {
					total += c.weight
				} else {
					gap := round - c.last
					if c.last == 0 {
						gap = round
					}
					total += math.Sqrt(float64(max(int64(1), gap)))
				}
			}
		}
		if total == 0 {
			return -1
		}
		pick := rand.Float64() * total
		for i, c := range candidates {
			if (priority && !c.priority) || (!priority && c.pending) {
				continue
			}
			w := c.weight
			if !priority {
				gap := round - c.last
				if c.last == 0 {
					gap = round
				}
				w = math.Sqrt(float64(max(int64(1), gap)))
			}
			pick -= w
			if pick <= 0 {
				return i
			}
		}
		return -1
	}
	for pos := 1; pos <= 6; pos++ {
		priority := pos <= 4
		idx := choose(priority)
		if idx < 0 && priority {
			priority = false
			idx = choose(false)
		}
		if idx < 0 {
			break
		}
		c := candidates[idx]
		candidates = append(candidates[:idx], candidates[idx+1:]...)
		slot := "routine"
		reason := "routine"
		if priority {
			slot = "priority"
			reason = c.reason
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO library_cleanup_batch(artist_id,position,slot_type,audit_reason,trigger_ai_id) VALUES(?,?,?,?,?)`, c.id, pos, slot, reason, c.aiID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO library_cleanup_skip(artist_id,until_round,last_batch_round) VALUES(?,0,?) ON CONFLICT(artist_id) DO UPDATE SET last_batch_round=excluded.last_batch_round`, c.id, round)
		if err != nil {
			return err
		}
	}
	return nil
}
func startCleanupCooldown(ctx context.Context, tx *sql.Tx, id, round int64, kept bool) error {
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE review_status='approved' AND collection_enabled=1`).Scan(&count); err != nil {
		return err
	}
	r := max(int64(1), count/6)
	rounds := max(int64(10), min(int64(35), r/4))
	minDays, maxDays := int64(14), int64(60)
	if kept {
		rounds = max(int64(30), min(int64(100), 3*r/4))
		minDays, maxDays = 60, 180
	}
	now := time.Now().UnixMilli()
	day := int64((24 * time.Hour) / time.Millisecond)
	_, err := tx.ExecContext(ctx, `INSERT INTO library_cleanup_skip(artist_id,until_round,started_at,min_at,max_at,last_batch_round,pending_reason,pending_ai_id) VALUES(?,?,?,?,?,?, '',NULL) ON CONFLICT(artist_id) DO UPDATE SET until_round=excluded.until_round,started_at=excluded.started_at,min_at=excluded.min_at,max_at=excluded.max_at,pending_reason='',pending_ai_id=NULL`, id, round+rounds, now, now+minDays*day, now+maxDays*day, round)
	return err
}
func coolCleanupBatch(ctx context.Context, tx *sql.Tx, round int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT b.artist_id FROM library_cleanup_batch b JOIN artists a ON a.id=b.artist_id WHERE b.kept=0 AND a.review_status='approved' AND a.collection_enabled=1`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = startCleanupCooldown(ctx, tx, id, round, false); err != nil {
			return err
		}
	}
	return nil
}
