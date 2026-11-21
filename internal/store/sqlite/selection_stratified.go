package sqlite

import (
	"context"
	"database/sql"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type stratifiedWork struct {
	id, artist, created int64
	bookmarks, views    sql.NullInt64
	pages               []int
	tags                []int64
	multiplier          float64
}
type stratifiedArtist struct {
	id     int64
	works  []*stratifiedWork
	weight float64
}
type tagVote struct{ likes, dislikes, documents float64 }
type feedbackWorkTag struct{ workID, tagID int64 }
type feedbackTagSnapshot struct {
	tagIDs    []int64
	items     []feedbackWorkTag
	expiresAt time.Time
}

// Tag associations only affect soft weights. Vote values and hard exclusions
// still come from the current transaction, and changed vote tags miss the cache.
func (s *Store) feedbackTagsForSelection(ctx context.Context, tx *sql.Tx, votes map[int64]*tagVote) ([]feedbackWorkTag, error) {
	ids := make([]int64, 0, len(votes))
	for id := range votes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	now := time.Now()
	if cached := s.stratifiedFeedbackTags.Load(); cached != nil && now.Before(cached.expiresAt) && slices.Equal(cached.tagIDs, ids) {
		return cached.items, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT at.illust_id,at.tag_id FROM artwork_tags at
		WHERE at.tag_id IN (SELECT DISTINCT v.tag_id FROM image_feedback f CROSS JOIN artwork_tags v ON v.illust_id=f.illust_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []feedbackWorkTag
	for rows.Next() {
		var item feedbackWorkTag
		if err = rows.Scan(&item.workID, &item.tagID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	s.stratifiedFeedbackTags.Store(&feedbackTagSnapshot{tagIDs: ids, items: items, expiresAt: now.Add(30 * time.Second)})
	return items, nil
}

// chooseStratified samples artist, artwork and page separately. Work multipliers
// appear in the artist's weighted mean and in the conditional work lottery;
// these factors cancel, so a multiplier affects the final probability once.
func (s *Store) chooseStratified(ctx context.Context, tx *sql.Tx, rules domain.SelectionRules, where string, args []any, artistID int64, accountID string, recentIDs []int64) (selectionPage, error) {
	works, ok, err := s.snapshotEligibleWorks(ctx, tx, rules, where, args, artistID, accountID, recentIDs)
	if err != nil {
		return selectionPage{}, err
	}
	if !ok {
		rows, err := tx.QueryContext(ctx, `SELECT a.id,a.artist_id,p.page_index,a.created_at,a.bookmarks,a.views FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where, args...)
		if err != nil {
			return selectionPage{}, err
		}
		works = map[int64]*stratifiedWork{}
		for rows.Next() {
			var id, artist int64
			var page int
			var created int64
			var bookmarks, views sql.NullInt64
			if err = rows.Scan(&id, &artist, &page, &created, &bookmarks, &views); err != nil {
				rows.Close()
				return selectionPage{}, err
			}
			w := works[id]
			if w == nil {
				w = &stratifiedWork{id: id, artist: artist, created: created, bookmarks: bookmarks, views: views, multiplier: 1}
				works[w.id] = w
			}
			w.pages = append(w.pages, page)
		}
		if err = rows.Close(); err != nil {
			return selectionPage{}, err
		}
		if err = rows.Err(); err != nil {
			return selectionPage{}, err
		}

	}
	if len(works) == 0 {
		return selectionPage{}, nil
	}
	var rows *sql.Rows

	votes := map[int64]*tagVote{}
	artistVotes := map[int64][2]float64{}
	rows, err = tx.QueryContext(ctx, `SELECT f.value,at.tag_id FROM image_feedback f JOIN artwork_tags at ON at.illust_id=f.illust_id`)
	if err != nil {
		return selectionPage{}, err
	}
	for rows.Next() {
		var value string
		var tag int64
		if err = rows.Scan(&value, &tag); err != nil {
			rows.Close()
			return selectionPage{}, err
		}
		v := votes[tag]
		if v == nil {
			v = &tagVote{}
			votes[tag] = v
		}
		if value == "like" {
			v.likes++
		} else {
			v.dislikes++
		}
	}
	if err = rows.Close(); err != nil {
		return selectionPage{}, err
	}
	if err = rows.Err(); err != nil {
		return selectionPage{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT a.artist_id,SUM(CASE f.value WHEN 'like' THEN 1 ELSE 0 END),SUM(CASE f.value WHEN 'dislike' THEN 1 ELSE 0 END) FROM image_feedback f JOIN artworks a ON a.id=f.illust_id GROUP BY a.artist_id`)
	if err != nil {
		return selectionPage{}, err
	}
	for rows.Next() {
		var id int64
		var pos, neg float64
		if err = rows.Scan(&id, &pos, &neg); err != nil {
			rows.Close()
			return selectionPage{}, err
		}
		artistVotes[id] = [2]float64{pos, neg}
	}
	if err = rows.Close(); err != nil {
		return selectionPage{}, err
	}
	if err = rows.Err(); err != nil {
		return selectionPage{}, err
	}
	if len(votes) > 0 {
		tags, err := s.feedbackTagsForSelection(ctx, tx, votes)
		if err != nil {
			return selectionPage{}, err
		}
		for _, tag := range tags {
			if w := works[tag.workID]; w != nil {
				w.tags = append(w.tags, tag.tagID)
				votes[tag.tagID].documents++
			}
		}
	}
	now := time.Now()
	workExposure := map[int64]int64{}
	rows, err = tx.QueryContext(ctx, `SELECT illust_id,exposed_at FROM exposure_work WHERE exposed_at>=?`, now.Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return selectionPage{}, err
	}
	for rows.Next() {
		var id, exposed int64
		if err = rows.Scan(&id, &exposed); err != nil {
			rows.Close()
			return selectionPage{}, err
		}
		workExposure[id] = exposed
	}
	if err = rows.Close(); err != nil {
		return selectionPage{}, err
	}
	if err = rows.Err(); err != nil {
		return selectionPage{}, err
	}
	artistExposure := map[int64]int64{}
	rows, err = tx.QueryContext(ctx, `SELECT artist_id,exposed_at FROM exposure_artist WHERE orientation=? AND exposed_at>=?`, normalizedOrientation(rules.Orientation), now.Add(-12*time.Hour).UnixMilli())
	if err != nil {
		return selectionPage{}, err
	}
	for rows.Next() {
		var id, exposed int64
		if err = rows.Scan(&id, &exposed); err != nil {
			rows.Close()
			return selectionPage{}, err
		}
		artistExposure[id] = exposed
	}
	if err = rows.Close(); err != nil {
		return selectionPage{}, err
	}
	if err = rows.Err(); err != nil {
		return selectionPage{}, err
	}
	var rates []float64
	if rules.Strategy == "popular" {
		for _, w := range works {
			if w.views.Valid && w.views.Int64 > 0 && w.bookmarks.Valid && w.bookmarks.Int64 >= 0 {
				rates = append(rates, float64(w.bookmarks.Int64)/float64(w.views.Int64))
			}
		}
	}
	sort.Float64s(rates)
	median := 0.0
	if len(rates) > 0 {
		median = rates[len(rates)/2]
		if len(rates)%2 == 0 {
			median = (median + rates[len(rates)/2-1]) / 2
		}
	}
	for _, w := range works {
		var bias, norm float64
		for _, tag := range w.tags {
			if v := votes[tag]; v != nil {
				n := v.likes + v.dislikes
				idf := math.Log(1 + float64(len(works))/(1+v.documents))
				bias += idf * ((v.likes - v.dislikes) / (n + 30))
				norm += idf
			}
		}
		if norm > 0 {
			w.multiplier = math.Max(.75, math.Min(1.35, math.Exp(.35*bias/norm)))
		}
		if rules.Strategy == "popular" && median > 0 && w.views.Valid && w.views.Int64 > 0 && w.bookmarks.Valid && w.bookmarks.Int64 >= 0 {
			rate := (float64(w.bookmarks.Int64) + median*1000) / (float64(w.views.Int64) + 1000)
			w.multiplier *= math.Max(.5, math.Min(2, rate/median))
		}
		w.multiplier *= cooldownFactor(now, workExposure[w.id], 24*time.Hour, 7*24*time.Hour, .25, .40)
	}
	// Recent additions receive a share proportional to the number of eligible
	// works, while an empty pool always returns its share to the full catalog.
	all := make([]*stratifiedWork, 0, len(works))
	recent := make([]*stratifiedWork, 0)
	unexposed := make([]*stratifiedWork, 0)
	cutoff := now.Add(-30 * 24 * time.Hour).UnixMilli()
	for _, w := range works {
		all = append(all, w)
		if workExposure[w.id] < cutoff {
			unexposed = append(unexposed, w)
		}
		if w.created >= cutoff {
			recent = append(recent, w)
		}
	}
	pool := all
	draw := rand.Float64()
	recentShare := .1 * math.Min(1, float64(len(recent))/300)
	if len(recent) > 0 && draw < recentShare {
		pool = recent
	} else if len(unexposed) > 0 && draw < recentShare+.05 {
		pool = unexposed
	}
	artistCooling := map[int64]float64{}
	for id, exposed := range artistExposure {
		artistCooling[id] = cooldownFactor(now, exposed, 3*time.Hour, 12*time.Hour, .6, .8)
	}
	// Fetch full page data only after selection and recheck live eligibility.
	for len(pool) > 0 {
		picked := stratifiedLottery(pool, artistVotes, artistCooling)
		pointArgs := append(append([]any(nil), args...), picked.page.IllustID, picked.page.PageIndex)
		rows, err := querySelectionRows(ctx, tx, selectionPageQuery(appendAnd(where, "p.illust_id=? AND p.page_index=?"), ""), pointArgs)
		if err != nil {
			return selectionPage{}, err
		}
		if len(rows) > 0 && domain.ValidImageURL(rows[0].page.OriginalURL) {
			return rows[0], nil
		}
		work := works[picked.page.IllustID]
		for i, page := range work.pages {
			if page == picked.page.PageIndex {
				work.pages = append(work.pages[:i], work.pages[i+1:]...)
				break
			}
		}
		if len(work.pages) == 0 {
			for i, w := range pool {
				if w == work {
					pool = append(pool[:i], pool[i+1:]...)
					break
				}
			}
		}
	}
	return selectionPage{}, nil
}

func stratifiedLottery(pool []*stratifiedWork, votes map[int64][2]float64, cooling map[int64]float64) selectionPage {
	artists := map[int64]*stratifiedArtist{}
	for _, w := range pool {
		a := artists[w.artist]
		if a == nil {
			a = &stratifiedArtist{id: w.artist}
			artists[w.artist] = a
		}
		a.works = append(a.works, w)
	}
	list := make([]*stratifiedArtist, 0, len(artists))
	weights := make([]float64, 0, len(artists))
	for _, a := range artists {
		var sumP, sumPM float64
		for _, w := range a.works {
			p := math.Pow(float64(len(w.pages)), .2)
			sumP += p
			sumPM += p * w.multiplier
		}
		v := votes[a.id]
		artistFeedback := math.Exp(.2 * (v[0] - v[1]) / (v[0] + v[1] + 30))
		factor := cooling[a.id]
		if factor == 0 {
			factor = 1
		}
		a.weight = math.Sqrt(float64(len(a.works))) * artistFeedback * factor * sumPM / sumP
		list = append(list, a)
		weights = append(weights, a.weight)
	}
	if len(list) == 0 {
		return selectionPage{}
	}
	a := list[weightedIndex(weights)]
	weights = weights[:0]
	for _, w := range a.works {
		weights = append(weights, math.Pow(float64(len(w.pages)), .2)*w.multiplier)
	}
	w := a.works[weightedIndex(weights)]
	return selectionPage{page: domain.SelectedPage{IllustID: w.id, ArtistID: w.artist, PageIndex: w.pages[rand.IntN(len(w.pages))]}}
}
func weightedIndex(weights []float64) int {
	var total float64
	for _, w := range weights {
		total += w
	}
	draw := rand.Float64() * total
	for i, w := range weights {
		draw -= w
		if draw < 0 {
			return i
		}
	}
	return len(weights) - 1
}

// cooldownFactor linearly interpolates the specified anchor points and treats
// missing exposure as fully recovered.
func cooldownFactor(now time.Time, exposed int64, middle, end time.Duration, startFactor, middleFactor float64) float64 {
	if exposed <= 0 {
		return 1
	}
	elapsed := now.Sub(time.UnixMilli(exposed))
	if elapsed <= 0 {
		return startFactor
	}
	if elapsed < middle {
		return startFactor + (middleFactor-startFactor)*float64(elapsed)/float64(middle)
	}
	if elapsed < end {
		return middleFactor + (1-middleFactor)*float64(elapsed-middle)/float64(end-middle)
	}
	return 1
}
