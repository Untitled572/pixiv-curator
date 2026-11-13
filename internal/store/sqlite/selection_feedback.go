package sqlite

import (
	"context"
	"database/sql"
	"math"
	"math/rand/v2"

	"github.com/Untitled572/pixiv-curator/internal/feedback"
)

func imageFeedbackInTx(ctx context.Context, tx *sql.Tx) (map[string]float64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.normalized_name,SUM(CASE f.value WHEN 'like' THEN 1.0 ELSE -1.0 END) score FROM image_feedback f JOIN artwork_tags at ON at.illust_id=f.illust_id JOIN tags t ON t.id=at.tag_id GROUP BY t.normalized_name ORDER BY ABS(score) DESC,t.normalized_name LIMIT 10000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]float64{}
	for rows.Next() {
		var tag string
		var n float64
		if err = rows.Scan(&tag, &n); err != nil {
			return nil, err
		}
		counts[tag] = n
	}
	return counts, rows.Err()
}

func feedbackLottery(weights []float64) []int {
	order := make([]int, 0, len(weights))
	remaining := append([]float64(nil), weights...)
	for range weights {
		var total float64
		for _, w := range remaining {
			total += w
		}
		if total <= 0 {
			break
		}
		draw := rand.Float64() * total
		for i, w := range remaining {
			draw -= w
			if draw < 0 {
				order = append(order, i)
				remaining[i] = 0
				break
			}
		}
	}
	return order
}

func feedbackOrderArtists(ctx context.Context, tx *sql.Tx, ids []int64, counts map[string]float64) ([]int64, error) {
	if len(counts) == 0 || len(ids) < 2 {
		return ids, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, `WITH frequencies AS (
	 SELECT w.artist_id,t.normalized_name,COUNT(*) n FROM artworks w JOIN artwork_tags at ON at.illust_id=w.id JOIN tags t ON t.id=at.tag_id WHERE w.visible=1 AND w.artist_id IN (`+placeholders(len(ids))+`) GROUP BY w.artist_id,t.normalized_name
	), ranked AS (SELECT *,ROW_NUMBER() OVER(PARTITION BY artist_id ORDER BY n DESC,normalized_name) rn FROM frequencies) SELECT artist_id,normalized_name FROM ranked WHERE rn<=8`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := map[int64][]string{}
	for rows.Next() {
		var id int64
		var tag string
		if err = rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		tags[id] = append(tags[id], tag)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	weights := make([]float64, len(ids))
	for i, id := range ids {
		weights[i] = math.Exp(.5 * feedback.LogBias(counts, tags[id]))
	}
	ordered := make([]int64, 0, len(ids))
	for _, i := range feedbackLottery(weights) {
		ordered = append(ordered, ids[i])
	}
	return ordered, nil
}

func feedbackOrderPages(ctx context.Context, tx *sql.Tx, pages []selectionPage, counts map[string]float64, scale float64) ([]selectionPage, error) {
	if len(counts) == 0 || len(pages) < 2 {
		return pages, nil
	}
	args := make([]any, len(pages))
	for i, p := range pages {
		args[i] = p.page.IllustID
	}
	rows, err := tx.QueryContext(ctx, `SELECT at.illust_id,t.normalized_name FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id IN (`+placeholders(len(pages))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := map[int64][]string{}
	for rows.Next() {
		var id int64
		var tag string
		if err = rows.Scan(&id, &tag); err != nil {
			return nil, err
		}
		tags[id] = append(tags[id], tag)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	weights := make([]float64, len(pages))
	for i, p := range pages {
		weights[i] = math.Exp(scale * feedback.LogBias(counts, tags[p.page.IllustID]))
	}
	ordered := make([]selectionPage, 0, len(pages))
	for _, i := range feedbackLottery(weights) {
		ordered = append(ordered, pages[i])
	}
	return ordered, nil
}
