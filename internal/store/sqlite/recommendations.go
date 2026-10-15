package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func checkRecommendationJobLease(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	if j.Kind != "recommend_artists" {
		return domain.ErrLease
	}
	return checkLease(ctx, tx, j)
}

func checkRecommendationLease(ctx context.Context, tx *sql.Tx, j domain.Job, runID string) error {
	if j.Kind != "recommend_artists" {
		return domain.ErrLease
	}
	if err := checkLease(ctx, tx, j); err != nil {
		return err
	}
	var payload struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.RunID != runID {
		return domain.ErrLease
	}
	return nil
}

func (s *Store) StartRecommendation(ctx context.Context, budget json.RawMessage) (string, string, error) {
	if len(budget) == 0 || !json.Valid(budget) {
		return "", "", domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var runID, jobID string
	err = tx.QueryRowContext(ctx, `SELECT r.id,j.id FROM recommendation_runs r JOIN jobs j ON j.kind='recommend_artists' AND json_extract(j.payload_json,'$.run_id')=r.id WHERE r.state='running' AND j.state IN ('pending','running','retry_wait','paused_auth') ORDER BY r.started_at DESC LIMIT 1`).Scan(&runID, &jobID)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return "", "", err
		}
		return runID, jobID, nil
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}
	var recent int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendation_runs WHERE state='succeeded' AND finished_at>?`, time.Now().Add(-time.Hour).UnixMilli()).Scan(&recent)
	if err != nil {
		return "", "", err
	}
	if recent > 0 {
		return "", "", domain.ErrConflict
	}
	runID = randomID()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at) VALUES(?,'running','v1',?,?)`, runID, string(budget), now); err != nil {
		return "", "", err
	}
	payload, _ := json.Marshal(map[string]string{"run_id": runID})
	jobID, err = enqueue(ctx, tx, "recommend_artists", "recommend:"+runID, payload)
	if err != nil {
		return "", "", err
	}
	return runID, jobID, tx.Commit()
}

func (s *Store) RecommendationSeeds(ctx context.Context, limit int) ([]domain.Artist, error) {
	if limit < 1 {
		return nil, domain.ErrInvalid
	}
	if limit > 30 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,account,avatar_url,review_status,collection_enabled,remote_followed FROM artists WHERE (review_status='approved' OR remote_followed=1) AND review_status NOT IN ('blocked','rejected') ORDER BY id LIMIT ?`, limit)
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

func (s *Store) RecommendationRun(ctx context.Context, id string) (domain.RecommendationRun, error) {
	var r domain.RecommendationRun
	var budget string
	err := s.db.QueryRowContext(ctx, `SELECT r.id,CASE WHEN r.state IN ('succeeded','partial') THEN r.state ELSE COALESCE(j.state,r.state) END,r.seed_cursor,r.candidate_cursor,r.request_count,r.budget_json FROM recommendation_runs r LEFT JOIN jobs j ON j.kind='recommend_artists' AND json_extract(j.payload_json,'$.run_id')=r.id WHERE r.id=? ORDER BY j.created_at DESC LIMIT 1`, id).Scan(&r.ID, &r.State, &r.SeedCursor, &r.CandidateCursor, &r.RequestCount, &budget)
	r.Budget = json.RawMessage(budget)
	if err == sql.ErrNoRows {
		err = domain.ErrNotFound
	}
	return r, err
}

func (s *Store) ReserveRecommendationRequest(ctx context.Context, j domain.Job, runID string, max int) (bool, error) {
	if max < 1 || max > 300 {
		return false, domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = checkRecommendationLease(ctx, tx, j, runID); err != nil {
		return false, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE recommendation_runs SET request_count=request_count+1 WHERE id=? AND state='running' AND request_count<?`, runID, max)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, tx.Commit()
}

func (s *Store) CommitRecommendationRecall(ctx context.Context, j domain.Job, runID string, nextSeed int64, candidates map[int64]domain.Artist, sources map[int64][]string, maxCandidates int) error {
	if runID == "" || nextSeed < 0 || maxCandidates < 0 {
		return domain.ErrInvalid
	}
	if maxCandidates > 60 {
		maxCandidates = 60
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRecommendationLease(ctx, tx, j, runID); err != nil {
		return err
	}
	var runState string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM recommendation_runs WHERE id=?`, runID).Scan(&runState); err != nil {
		return err
	}
	if runState != "running" {
		return domain.ErrConflict
	}
	ids := make([]int64, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		a := candidates[id]
		if id <= 0 || a.ID != id {
			return domain.ErrInvalid
		}

		var status string
		statusErr := tx.QueryRowContext(ctx, `SELECT review_status FROM artists WHERE id=?`, id).Scan(&status)
		if statusErr != nil && statusErr != sql.ErrNoRows {
			return statusErr
		}
		eligible := statusErr == sql.ErrNoRows || status != "approved" && status != "rejected" && status != "blocked"
		var oldJSON string
		sourceErr := tx.QueryRowContext(ctx, `SELECT sources_json FROM recommendations WHERE run_id=? AND artist_id=?`, runID, id).Scan(&oldJSON)
		if sourceErr != nil && sourceErr != sql.ErrNoRows {
			return sourceErr
		}
		if eligible && sourceErr == sql.ErrNoRows {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations WHERE run_id=?`, runID).Scan(&count); err != nil {
				return err
			}
			if count >= maxCandidates {
				continue
			}
		}

		now := time.Now().UnixMilli()
		if _, err = tx.ExecContext(ctx, `INSERT INTO artists(id,name,account,avatar_url,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,account=excluded.account,avatar_url=excluded.avatar_url,updated_at=excluded.updated_at`, a.ID, a.Name, a.Account, a.AvatarURL, now, now); err != nil {
			return err
		}
		for _, src := range dedupeStrings(sources[id]) {
			key := runID + ":" + src
			if _, err = tx.ExecContext(ctx, `INSERT INTO artist_sources(artist_id,source_type,source_key,first_seen_at) VALUES(?,'recommend',?,?) ON CONFLICT DO NOTHING`, id, key, now); err != nil {
				return err
			}
		}
		if !eligible {
			continue
		}
		var oldSources []string
		if oldJSON != "" {
			_ = json.Unmarshal([]byte(oldJSON), &oldSources)
		}
		sourceList := dedupeStrings(append(oldSources, sources[id]...))
		srcJSON, _ := json.Marshal(sourceList)
		empty, _ := json.Marshal([]string{})
		_, err = tx.ExecContext(ctx, `INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,0,'[]',?,?, 'low','', '{}',?,?) ON CONFLICT(run_id,artist_id) DO UPDATE SET sources_json=excluded.sources_json,updated_at=excluded.updated_at`, runID, id, string(empty), string(srcJSON), now, now)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET seed_cursor=MAX(seed_cursor,?) WHERE id=?`, nextSeed, runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func (s *Store) RecommendationCandidates(ctx context.Context, runID string, after int64) ([]domain.Recommendation, error) {
	if after < 0 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.run_id,r.artist_id,r.score,r.reasons_json,r.sources_json,r.sample_illust_ids_json,r.confidence,r.score_version,r.components_json,a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND r.artist_id>? AND a.review_status='pending' ORDER BY r.artist_id LIMIT 60`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Recommendation{}
	for rows.Next() {
		var r domain.Recommendation
		var reasons, sources, samples, components string
		if err = rows.Scan(&r.RunID, &r.ArtistID, &r.Score, &reasons, &sources, &samples, &r.Confidence, &r.Version, &components, &r.Artist.ID, &r.Artist.Name, &r.Artist.Account, &r.Artist.AvatarURL, &r.Artist.ReviewStatus, &r.Artist.CollectionEnabled, &r.Artist.RemoteFollowed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(reasons), &r.Reasons)
		_ = json.Unmarshal([]byte(sources), &r.Sources)
		_ = json.Unmarshal([]byte(samples), &r.SampleIDs)
		r.Components = json.RawMessage(components)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CommitRecommendationSamples(ctx context.Context, j domain.Job, runID string, artistID, nextID int64, artworks []domain.Artwork, result domain.Recommendation) error {
	if runID == "" || artistID <= 0 || nextID < 0 || result.ArtistID != artistID || result.Score < 0 || result.Score > 1 || len(artworks) > 4 {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRecommendationLease(ctx, tx, j, runID); err != nil {
		return err
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT review_status='pending' FROM artists WHERE id=?`, artistID).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		if _, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET candidate_cursor=MAX(candidate_cursor,?) WHERE id=?`, nextID, runID); err != nil {
			return err
		}
		return tx.Commit()
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recommendations WHERE run_id=? AND artist_id=?)`, runID, artistID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return domain.ErrNotFound
	}
	for _, a := range artworks {
		if a.ArtistID != artistID {
			return domain.ErrInvalid
		}
		if err = upsertArtwork(ctx, tx, a); err != nil {
			return err
		}
	}
	reasons, _ := json.Marshal(result.Reasons)
	sources, _ := json.Marshal(dedupeStrings(result.Sources))
	samples, _ := json.Marshal(result.SampleIDs)
	components := result.Components
	if len(components) == 0 || !json.Valid(components) {
		components = json.RawMessage(`{}`)
	}
	_, err = tx.ExecContext(ctx, `UPDATE recommendations SET score=?,reasons_json=?,sources_json=?,sample_illust_ids_json=?,confidence=?,score_version=?,components_json=?,updated_at=? WHERE run_id=? AND artist_id=?`, result.Score, string(reasons), string(sources), string(samples), result.Confidence, result.Version, string(components), time.Now().UnixMilli(), runID, artistID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET candidate_cursor=MAX(candidate_cursor,?) WHERE id=?`, nextID, runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitSeedSamples(ctx context.Context, j domain.Job, artistID int64, artworks []domain.Artwork) error {
	if artistID <= 0 || len(artworks) > 8 {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRecommendationJobLease(ctx, tx, j); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT (review_status='approved' OR remote_followed=1) AND review_status NOT IN ('blocked','rejected') FROM artists WHERE id=?`, artistID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
	}
	for _, a := range artworks {
		if a.ArtistID != artistID {
			return domain.ErrInvalid
		}
		if err = upsertArtwork(ctx, tx, a); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CompleteRecommendation(ctx context.Context, j domain.Job, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRecommendationLease(ctx, tx, j, runID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	r, err := tx.ExecContext(ctx, `UPDATE recommendation_runs SET state='succeeded',finished_at=? WHERE id=? AND state='running'`, now, runID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrConflict
	}
	if err = completeJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LatestRecommendation(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM recommendation_runs ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", domain.ErrNotFound
	}
	return id, err
}
