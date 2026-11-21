package sqlite

import (
	"context"
	"database/sql"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type stratifiedWork struct {
	id, artist, created int64
	bookmarks, views    sql.NullInt64
	pages               []selectionPage
	tags                []string
	multiplier          float64
}
type stratifiedArtist struct {
	id     int64
	works  []*stratifiedWork
	weight float64
}
type tagVote struct{ likes, dislikes, documents float64 }

// chooseStratified samples artist, artwork and page separately. Work multipliers
// appear in the artist's weighted mean and in the conditional work lottery;
// these factors cancel, so a multiplier affects the final probability once.
func (s *Store) chooseStratified(ctx context.Context, tx *sql.Tx, rules domain.SelectionRules, where string, args []any, artistID int64, accountID string, recentIDs []int64) (selectionPage, error) {
	works, ok, err := s.snapshotEligibleWorks(ctx, tx, rules, where, args, artistID, accountID, recentIDs)
	if err != nil {
		return selectionPage{}, err
	}
	if !ok {
		rows, err := tx.QueryContext(ctx, `SELECT `+selectionColumns+`,a.created_at,a.bookmarks,a.views FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where, args...)
		if err != nil {
			return selectionPage{}, err
		}
		works = map[int64]*stratifiedWork{}
		for rows.Next() {
			var p selectionPage
			var created int64
			var bookmarks, views sql.NullInt64
			if err = rows.Scan(&p.page.IllustID, &p.page.ArtistID, &p.page.PageIndex, &p.page.Width, &p.page.Height, &p.page.OriginalURL, &p.page.PreviewURL, &created, &bookmarks, &views); err != nil {
				rows.Close()
				return selectionPage{}, err
			}
			if !domain.ValidImageURL(p.page.OriginalURL) {
				continue
			}
			w := works[p.page.IllustID]
			if w == nil {
				w = &stratifiedWork{id: p.page.IllustID, artist: p.page.ArtistID, created: created, bookmarks: bookmarks, views: views, multiplier: 1}
				works[w.id] = w
			}
			w.pages = append(w.pages, p)
		}
		if err = rows.Close(); err != nil {
			return selectionPage{}, err
		}
		if err = rows.Err(); err != nil {
			return selectionPage{}, err
		}
		if len(works) == 0 {
			return selectionPage{}, nil
		}
		// Only tags of eligible works participate in each lottery; feedback remains
		// global and is never used as a hard filter beyond the page exclusion in SQL.
		rows, err = tx.QueryContext(ctx, `SELECT at.illust_id,t.normalized_name FROM artwork_tags at JOIN tags t ON t.id=at.tag_id`)
		if err != nil {
			return selectionPage{}, err
		}
		for rows.Next() {
			var id int64
			var tag string
			if err = rows.Scan(&id, &tag); err != nil {
				rows.Close()
				return selectionPage{}, err
			}
			if w := works[id]; w != nil {
				w.tags = append(w.tags, tag)
			}
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

	votes := map[string]*tagVote{}
	artistVotes := map[int64][2]float64{}
	rows, err = tx.QueryContext(ctx, `SELECT a.artist_id,f.value,t.normalized_name FROM image_feedback f JOIN artworks a ON a.id=f.illust_id LEFT JOIN artwork_tags at ON at.illust_id=a.id LEFT JOIN tags t ON t.id=at.tag_id`)
	if err != nil {
		return selectionPage{}, err
	}
	for rows.Next() {
		var id int64
		var value string
		var tag sql.NullString
		if err = rows.Scan(&id, &value, &tag); err != nil {
			rows.Close()
			return selectionPage{}, err
		}
		if tag.Valid {
			v := votes[tag.String]
			if v == nil {
				v = &tagVote{}
				votes[tag.String] = v
			}
			if value == "like" {
				v.likes++
			} else {
				v.dislikes++
			}
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
	for _, w := range works {
		for _, tag := range w.tags {
			if v := votes[tag]; v != nil {
				v.documents++
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
	for _, w := range works {
		if w.views.Valid && w.views.Int64 > 0 && w.bookmarks.Valid && w.bookmarks.Int64 >= 0 {
			rates = append(rates, float64(w.bookmarks.Int64)/float64(w.views.Int64))
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
	return stratifiedLottery(pool, artistVotes, artistCooling), nil
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
	return w.pages[rand.IntN(len(w.pages))]
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
