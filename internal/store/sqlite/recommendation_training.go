package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
)

const (
	regressionArtistsPerStatus = 1000
	regressionRecentWorks      = 100
	regressionTagsPerArtist    = 32
)

var regressionOfficialSources = []string{
	recommend.SourceRelatedArtist,
	recommend.SourceRelatedArtwork,
	recommend.SourceRecommendationFeed,
	recommend.SourceTagSearch,
}

// RecommendationRegressionExamples builds one explicit feedback example per
// currently approved or rejected artist. It reads local state only and never
// treats pending/blocked artists or recommendation exposure as feedback.
func (s *Store) RecommendationRegressionExamples(ctx context.Context, runID string) ([]recommend.RegressionExample, error) {
	if runID == "" {
		return nil, domain.ErrInvalid
	}
	var budgetJSON string
	err := s.db.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&budgetJSON)
	if err == sql.ErrNoRows {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var budget struct {
		RecentApprovedArtistIDs []int64 `json:"recent_approved_artist_ids"`
		PreviousCandidateIDs    []int64 `json:"excluded_artist_ids"`
	}
	if err = json.Unmarshal([]byte(budgetJSON), &budget); err != nil {
		return nil, err
	}
	recentApproved := make(map[int64]struct{}, len(budget.RecentApprovedArtistIDs))
	for _, id := range append(budget.RecentApprovedArtistIDs, budget.PreviousCandidateIDs...) {
		if id > 0 {
			recentApproved[id] = struct{}{}
		}
	}

	selectedIDs, statuses, err := s.regressionTrainingArtists(ctx)
	if err != nil {
		return nil, err
	}
	if len(selectedIDs) == 0 {
		return []recommend.RegressionExample{}, nil
	}
	idsJSON, err := json.Marshal(selectedIDs)
	if err != nil {
		return nil, err
	}
	tagsByArtist, err := s.regressionTrainingTags(ctx, string(idsJSON))
	if err != nil {
		return nil, err
	}
	sourcesByArtist, err := s.regressionTrainingSources(ctx, string(idsJSON))
	if err != nil {
		return nil, err
	}

	examples := make([]recommend.RegressionExample, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		tags := tagsByArtist[id]
		sources := sourcesByArtist[id]
		if len(tags) == 0 && len(sources) == 0 {
			continue
		}
		status := statuses[id]
		example := recommend.RegressionExample{
			Tags:    tags,
			Sources: sources,
			Liked:   status == "approved",
			Weight:  1,
		}
		if status == "approved" {
			if _, recent := recentApproved[id]; recent {
				example.Weight = 0.2
			}
		}
		examples = append(examples, example)
	}
	return examples, nil
}

func (s *Store) regressionTrainingArtists(ctx context.Context) ([]int64, map[int64]string, error) {
	query := `WITH qualified AS (
		SELECT a.id,a.review_status FROM artists a
		WHERE a.review_status IN ('approved','rejected')
		AND (
			EXISTS(SELECT 1 FROM artworks w JOIN artwork_tags at ON at.illust_id=w.id WHERE w.artist_id=a.id AND w.visible=1)
			OR EXISTS(
				SELECT 1 FROM recommendations r,json_each(r.sources_json) src
				WHERE r.artist_id=a.id AND json_type(r.sources_json)='array' AND src.type='text' AND src.value IN (?,?,?,?)
			)
		)
	), ranked AS (
		SELECT id,review_status,ROW_NUMBER() OVER(PARTITION BY review_status ORDER BY id) AS status_rank FROM qualified
	)
	SELECT id,review_status FROM ranked WHERE status_rank<=? ORDER BY id`
	args := []any{regressionOfficialSources[0], regressionOfficialSources[1], regressionOfficialSources[2], regressionOfficialSources[3], regressionArtistsPerStatus}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, 2*regressionArtistsPerStatus)
	statuses := make(map[int64]string, 2*regressionArtistsPerStatus)
	for rows.Next() {
		var id int64
		var status string
		if err = rows.Scan(&id, &status); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
		statuses[id] = status
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	return ids, statuses, nil
}

func (s *Store) regressionTrainingTags(ctx context.Context, idsJSON string) (map[int64][]string, error) {
	query := `WITH selected AS (
		SELECT CAST(value AS INTEGER) AS artist_id FROM json_each(?)
	), recent AS (
		SELECT w.artist_id,w.id,ROW_NUMBER() OVER(PARTITION BY w.artist_id ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC) AS work_rank
		FROM artworks w JOIN selected s ON s.artist_id=w.artist_id WHERE w.visible=1
	), frequency AS (
		SELECT r.artist_id,t.normalized_name,COUNT(*) AS uses
		FROM recent r JOIN artwork_tags at ON at.illust_id=r.id JOIN tags t ON t.id=at.tag_id
		WHERE r.work_rank<=?
		GROUP BY r.artist_id,t.normalized_name
	), ranked AS (
		SELECT artist_id,normalized_name,ROW_NUMBER() OVER(PARTITION BY artist_id ORDER BY uses DESC,normalized_name) AS tag_rank
		FROM frequency
	)
	SELECT artist_id,normalized_name FROM ranked WHERE tag_rank<=? ORDER BY artist_id,tag_rank`
	rows, err := s.db.QueryContext(ctx, query, idsJSON, regressionRecentWorks, regressionTagsPerArtist)
	if err != nil {
		return nil, err
	}
	tags := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var tag string
		if err = rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return nil, err
		}
		tags[id] = append(tags[id], tag)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return tags, nil
}

func (s *Store) regressionTrainingSources(ctx context.Context, idsJSON string) (map[int64][]string, error) {
	query := `SELECT DISTINCT r.artist_id,src.value
		FROM recommendations r
		JOIN json_each(r.sources_json) src
		JOIN json_each(?) selected ON CAST(selected.value AS INTEGER)=r.artist_id
		WHERE json_type(r.sources_json)='array' AND src.type='text' AND src.value IN (?,?,?,?)
		ORDER BY r.artist_id,src.value`
	args := []any{idsJSON, regressionOfficialSources[0], regressionOfficialSources[1], regressionOfficialSources[2], regressionOfficialSources[3]}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	sources := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var source string
		if err = rows.Scan(&id, &source); err != nil {
			rows.Close()
			return nil, err
		}
		sources[id] = append(sources[id], source)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for id := range sources {
		sort.Strings(sources[id])
	}
	return sources, nil
}
