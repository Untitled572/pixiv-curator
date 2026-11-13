package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"math"
	"sort"
)

// Freeze diverse seeds at creation; a resumed run must never pick a different set.
func freezeRecommendationSeeds(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (json.RawMessage, error) {
	var b map[string]json.RawMessage
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, domain.ErrInvalid
	}
	if b == nil {
		b = make(map[string]json.RawMessage)
	}
	var limit int
	if rawLimit, exists := b["seed_artists"]; exists {
		if err := json.Unmarshal(rawLimit, &limit); err != nil || limit < 0 {
			return nil, domain.ErrInvalid
		}
	}
	if limit > 30 {
		return nil, domain.ErrInvalid
	}
	// Only the immediately previous successful round contributes recent approvals.
	recentIDs := []int64{}
	var previous string
	err := tx.QueryRowContext(ctx, `SELECT id FROM recommendation_runs WHERE state='succeeded' ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if previous != "" {
		rows, queryErr := tx.QueryContext(ctx, `SELECT DISTINCT r.artist_id FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='approved' ORDER BY r.artist_id`, previous)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var id int64
			if queryErr = rows.Scan(&id); queryErr != nil {
				rows.Close()
				return nil, queryErr
			}
			recentIDs = append(recentIDs, id)
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return nil, queryErr
		}
		rows.Close()
	}
	// Classify each long-term approved artist from up to 100 most recent visible
	// works. Adult evidence wins; otherwise only an all-safe sample is safe.
	const classified = `WITH ranked AS (
		SELECT artist_id,rating,row_number() OVER(PARTITION BY artist_id ORDER BY COALESCE(published_at,0) DESC,id DESC) AS rn
		FROM artworks WHERE visible=1
	), per_artist AS (
		SELECT artist_id,CASE WHEN MAX(rating IN ('r18','r18g'))=1 THEN 'adult'
			WHEN MIN(rating='safe')=1 THEN 'safe' ELSE 'unknown' END AS content_class
		FROM ranked WHERE rn<=100 GROUP BY artist_id
	)`
	var adultCount, classifiableCount float64
	recentJSON, _ := json.Marshal(recentIDs)
	if err = tx.QueryRowContext(ctx, classified+`, weighted AS (SELECT content_class,CASE WHEN a.id IN (SELECT CAST(value AS INTEGER) FROM json_each(?)) THEN 0.2 ELSE 1.0 END weight FROM per_artist JOIN artists a ON a.id=per_artist.artist_id WHERE a.review_status='approved' AND content_class IN ('adult','safe')) SELECT COALESCE(SUM(weight*(content_class='adult')),0),COALESCE(SUM(weight),0) FROM weighted`, string(recentJSON)).Scan(&adultCount, &classifiableCount); err != nil {
		return nil, err
	}
	libraryAdultRatio := 0.5
	if classifiableCount > 0 {
		libraryAdultRatio = float64(adultCount) / float64(classifiableCount)
	}
	if math.IsNaN(libraryAdultRatio) || math.IsInf(libraryAdultRatio, 0) {
		libraryAdultRatio = 0.5
	}
	ids := []int64{}
	selected := map[int64]bool{}
	if limit > 0 {
		// Current approved artists, including paused ones, are eligible. Remote
		// followed artists retain their historical compatibility eligibility.
		const eligible = `WITH ranked AS (
			SELECT artist_id,rating,row_number() OVER(PARTITION BY artist_id ORDER BY COALESCE(published_at,0) DESC,id DESC) rn FROM artworks WHERE visible=1
		), per_artist AS (
			SELECT artist_id,CASE WHEN MAX(rating IN ('r18','r18g'))=1 THEN 'adult' WHEN MIN(rating='safe')=1 THEN 'safe' ELSE 'unknown' END content_class
			FROM ranked WHERE rn<=100 GROUP BY artist_id
		), eligible AS (
			SELECT a.id,a.review_status,a.collection_enabled,COALESCE(p.content_class,'unknown') content_class
			FROM artists a LEFT JOIN per_artist p ON p.artist_id=a.id
			WHERE (a.review_status='approved' OR a.remote_followed=1) AND a.review_status NOT IN ('blocked','rejected')
		)`
		queryIDs := func(class string, excludeRecent bool, n int) ([]int64, error) {
			if n <= 0 {
				return nil, nil
			}
			args := []any{}
			where := ` WHERE 1=1`
			if class != "" {
				where += ` AND content_class=?`
				args = append(args, class)
			}
			if excludeRecent && len(recentIDs) > 0 {
				where += ` AND id NOT IN (` + placeholders(len(recentIDs)) + `)`
				for _, id := range recentIDs {
					args = append(args, id)
				}
			}
			if len(selected) > 0 {
				where += ` AND id NOT IN (` + placeholders(len(selected)) + `)`
				for id := range selected {
					args = append(args, id)
				}
			}
			args = append(args, n)
			rows, qerr := tx.QueryContext(ctx, eligible+` SELECT id FROM eligible`+where+` ORDER BY (review_status='approved' AND collection_enabled=1) DESC,random() LIMIT ?`, args...)
			if qerr != nil {
				return nil, qerr
			}
			defer rows.Close()
			out := []int64{}
			for rows.Next() {
				var id int64
				if qerr = rows.Scan(&id); qerr != nil {
					return nil, qerr
				}
				out = append(out, id)
			}
			return out, rows.Err()
		}
		// Exclude recent approvals while there are enough other eligible seed
		// artists. Aim for the long-term adult/safe ratio; unknown stays a third
		// class and only fills slots when classified strata are insufficient.
		adultTarget := int(math.Round(float64(limit) * libraryAdultRatio))
		pickedAdult, err := queryIDs("adult", true, adultTarget)
		if err != nil {
			return nil, err
		}
		pickedSafe, err := queryIDs("safe", true, limit-adultTarget)
		if err != nil {
			return nil, err
		}
		ids = append(ids, pickedAdult...)
		ids = append(ids, pickedSafe...)
		for _, id := range ids {
			selected[id] = true
		}
		fill := func(class string, excludeRecent bool, n int) error {
			more, qerr := queryIDs(class, excludeRecent, n)
			if qerr != nil {
				return qerr
			}
			for _, id := range more {
				if len(ids) >= limit {
					break
				}
				if !selected[id] {
					selected[id] = true
					ids = append(ids, id)
				}
			}
			return nil
		}
		if len(ids) < limit {
			if len(pickedAdult) < adultTarget {
				if err = fill("safe", true, adultTarget-len(pickedAdult)); err != nil {
					return nil, err
				}
			}
			if len(pickedSafe) < limit-adultTarget {
				if err = fill("adult", true, limit-adultTarget-len(pickedSafe)); err != nil {
					return nil, err
				}
			}
		}
		if len(ids) < limit {
			if err = fill("unknown", true, limit-len(ids)); err != nil {
				return nil, err
			}
		}
		if len(ids) < limit {
			if err = fill("", true, limit-len(ids)); err != nil {
				return nil, err
			}
		}
		if len(ids) < limit && len(recentIDs) > 0 {
			for _, id := range recentIDs {
				if len(ids) >= limit {
					break
				}
				var eligibleRecent bool
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND review_status='approved')`, id).Scan(&eligibleRecent); err != nil {
					return nil, err
				}
				if eligibleRecent && !selected[id] {
					selected[id] = true
					ids = append(ids, id)
					break
				}
			}
			if len(ids) < limit {
				more, qerr := queryIDs("", false, limit-len(ids))
				if qerr != nil {
					return nil, qerr
				}
				for _, id := range more {
					if len(ids) >= limit {
						break
					}
					if !selected[id] {
						selected[id] = true
						ids = append(ids, id)
					}
				}
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	b["seed_artist_ids"] = encoded
	if recentIDs == nil {
		recentIDs = []int64{}
	}
	recentJSON, err = json.Marshal(recentIDs)
	if err != nil {
		return nil, err
	}
	b["recent_approved_artist_ids"] = recentJSON
	ratioJSON, err := json.Marshal(libraryAdultRatio)
	if err != nil {
		return nil, err
	}
	b["library_adult_ratio"] = ratioJSON
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
