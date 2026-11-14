package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	budget, err = freezeRecommendationExclusions(ctx, tx, budget, "")
	if err != nil {
		return "", "", err
	}
	budget, err = freezeRecommendationSeeds(ctx, tx, budget)
	if err != nil {
		return "", "", err
	}
	budget, err = freezeExplorationPlan(budget)
	if err != nil {
		return "", "", err
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
	rows, err := s.db.QueryContext(ctx, `WITH eligible AS (
		SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed,a.created_at,
			COALESCE(MAX(CASE WHEN aa.followed=1 AND ac.deleted=0 THEN aa.followed_at ELSE 0 END),0) AS followed_at
		FROM artists a
		LEFT JOIN account_artists aa ON aa.artist_id=a.id
		LEFT JOIN accounts ac ON ac.id=aa.account_id
		WHERE ((a.review_status='approved' AND a.collection_enabled=1) OR (a.review_status='pending' AND a.remote_followed=1))
		GROUP BY a.id
	), chosen AS (
		SELECT * FROM eligible
		ORDER BY CASE WHEN followed_at>0 THEN 0 ELSE 1 END,followed_at DESC,created_at DESC,id ASC
		LIMIT ?
	)
	SELECT id,name,account,avatar_url,review_status,collection_enabled,remote_followed FROM chosen ORDER BY id ASC`, limit)
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
	if err == nil {
		r.ProgressPercent, err = s.recommendationProgress(ctx, r)
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
	var budgetRaw string
	if err = tx.QueryRowContext(ctx, `SELECT budget_json FROM recommendation_runs WHERE id=?`, runID).Scan(&budgetRaw); err != nil {
		return err
	}
	excluded, err := recommendationExclusionSet(json.RawMessage(budgetRaw))
	if err != nil {
		return err
	}
	var plan struct {
		Independent  bool `json:"independent_exploration"`
		Limit        int  `json:"independent_candidate_limit"`
		RankingLimit int  `json:"ranking_candidate_limit"`
		UniformLimit int  `json:"uniform_candidate_limit"`
	}
	if err = json.Unmarshal([]byte(budgetRaw), &plan); err != nil {
		return err
	}
	ids := make([]int64, 0, len(candidates))
	independentIDs := make([]int64, 0, len(candidates))
	for id := range candidates {
		if plan.Independent && hasIndependentRecallSource(sources[id]) {
			independentIDs = append(independentIDs, id)
		} else {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sort.Slice(independentIDs, func(i, j int) bool {
		a, b := recommendationAdmissionHash(runID, independentIDs[i]), recommendationAdmissionHash(runID, independentIDs[j])
		if a == b {
			return independentIDs[i] < independentIDs[j]
		}
		return string(a[:]) < string(b[:])
	})
	ids = append(independentIDs, ids...)
	var rankingCohort, uniformCohort, totalIndependent int
	if plan.Independent {
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r WHERE r.run_id=? AND COALESCE(json_extract(r.components_json,'$.carryover'),0)<>1 AND EXISTS(SELECT 1 FROM json_each(r.sources_json) WHERE value=?)`, runID, independentSource).Scan(&rankingCohort); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r WHERE r.run_id=? AND COALESCE(json_extract(r.components_json,'$.carryover'),0)<>1 AND EXISTS(SELECT 1 FROM json_each(r.sources_json) WHERE value=?)`, runID, independentUniformSource).Scan(&uniformCohort); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r WHERE r.run_id=? AND COALESCE(json_extract(r.components_json,'$.carryover'),0)<>1 AND EXISTS(SELECT 1 FROM json_each(r.sources_json) WHERE value IN (?,?))`, runID, independentSource, independentUniformSource).Scan(&totalIndependent); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if excluded[id] {
			continue
		}
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
		var carryover bool
		sourceErr := tx.QueryRowContext(ctx, `SELECT sources_json,COALESCE(json_extract(components_json,'$.carryover'),0) FROM recommendations WHERE run_id=? AND artist_id=?`, runID, id).Scan(&oldJSON, &carryover)
		if sourceErr != nil && sourceErr != sql.ErrNoRows {
			return sourceErr
		}
		if sourceErr == nil && carryover {
			continue
		}
		var oldSources []string
		if oldJSON != "" {
			_ = json.Unmarshal([]byte(oldJSON), &oldSources)
		}
		mergedSources := dedupeStrings(append(oldSources, sources[id]...))
		oldRanking := hasIndependentRankingSource(oldSources)
		oldUniform := hasIndependentUniformSource(oldSources)
		newRanking := hasIndependentRankingSource(mergedSources) && !oldRanking
		newUniform := hasIndependentUniformSource(mergedSources) && !oldUniform
		newIndependent := !oldRanking && !oldUniform && (newRanking || newUniform)
		if eligible && plan.Independent && (newRanking || newUniform) {
			if newRanking && rankingCohort >= plan.RankingLimit {
				continue
			}
			if newUniform && uniformCohort >= plan.UniformLimit {
				continue
			}
			if newIndependent && totalIndependent >= plan.Limit {
				continue
			}
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
		srcJSON, _ := json.Marshal(mergedSources)
		empty, _ := json.Marshal([]string{})
		_, err = tx.ExecContext(ctx, `INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,0,'[]',?,?, 'low','', '{}',?,?) ON CONFLICT(run_id,artist_id) DO UPDATE SET sources_json=excluded.sources_json,updated_at=excluded.updated_at`, runID, id, string(empty), string(srcJSON), now, now)
		if err != nil {
			return err
		}
		if plan.Independent {
			if newRanking {
				rankingCohort++
			}
			if newUniform {
				uniformCohort++
			}
			if newIndependent {
				totalIndependent++
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET seed_cursor=MAX(seed_cursor,?) WHERE id=?`, nextSeed, runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func recommendationAdmissionHash(runID string, artistID int64) [32]byte {
	return sha256.Sum256([]byte(runID + ":" + strconv.FormatInt(artistID, 10)))
}

func hasIndependentRecallSource(sources []string) bool {
	return hasIndependentRankingSource(sources) || hasIndependentUniformSource(sources)
}

func hasIndependentRankingSource(sources []string) bool {
	return containsString(sources, independentSource)
}

func hasIndependentUniformSource(sources []string) bool {
	return containsString(sources, independentUniformSource)
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
	invalidSamples := make(map[int64]bool)
	unknownSampleCount := make(map[int64]int)
	for rows.Next() {
		var r domain.Recommendation
		var reasons, sources, samples, components string
		if err = rows.Scan(&r.RunID, &r.ArtistID, &r.Score, &reasons, &sources, &samples, &r.Confidence, &r.Version, &components, &r.Artist.ID, &r.Artist.Name, &r.Artist.Account, &r.Artist.AvatarURL, &r.Artist.ReviewStatus, &r.Artist.CollectionEnabled, &r.Artist.RemoteFollowed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(reasons), &r.Reasons)
		_ = json.Unmarshal([]byte(sources), &r.Sources)
		r.SampleIDs, invalidSamples[r.ArtistID], unknownSampleCount[r.ArtistID] = parseRecommendationSampleIDs(samples)
		r.Components = json.RawMessage(components)
		var flags struct {
			Exploration bool `json:"exploration"`
		}
		_ = json.Unmarshal(r.Components, &flags)
		r.Exploration = flags.Exploration
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// Query a normalized, bounded payload rather than re-reading raw JSON IDs.
	// Missing rows (including samples belonging to another artist) remain unknown.
	payload := make([]map[string]any, 0, len(out))
	byArtist := make(map[int64]*domain.Recommendation, len(out))
	for i := range out {
		payload = append(payload, map[string]any{"artist_id": out[i].ArtistID, "ids": out[i].SampleIDs})
		byArtist[out[i].ArtistID] = &out[i]
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sampleRows, err := s.db.QueryContext(ctx, `SELECT json_extract(c.value,'$.artist_id'),j.value,a.rating,a.ai_status,COALESCE((SELECT json_group_array(t.display_name) FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id=a.id),'[]')
 FROM json_each(?) c JOIN json_each(json_extract(c.value,'$.ids')) j
 JOIN artworks a ON a.id=CAST(j.value AS INTEGER) AND a.artist_id=json_extract(c.value,'$.artist_id')`, string(encoded))
	if err != nil {
		return nil, err
	}
	resolved := make(map[int64]int, len(out))
	tagCounts := make(map[int64]map[string]int, len(out))
	for sampleRows.Next() {
		var artistID int64
		var sampleID, rating, aiStatus, rawTags string
		if err = sampleRows.Scan(&artistID, &sampleID, &rating, &aiStatus, &rawTags); err != nil {
			sampleRows.Close()
			return nil, err
		}
		r := byArtist[artistID]
		var tags []string
		if err = json.Unmarshal([]byte(rawTags), &tags); err != nil {
			sampleRows.Close()
			return nil, err
		}
		if tagCounts[artistID] == nil {
			tagCounts[artistID] = map[string]int{}
		}
		for _, tag := range tags {
			tagCounts[artistID][tag]++
		}
		resolved[artistID]++
		switch rating {
		case "r18", "r18g":
			r.AdultCount++
		case "safe":
			r.SafeCount++
		default:
			r.UnknownCount++
		}
		switch aiStatus {
		case "ai":
			r.AICount++
		case "non_ai":
			r.NonAICount++
		default:
			r.UnknownAICount++
		}
	}
	if err = sampleRows.Err(); err != nil {
		sampleRows.Close()
		return nil, err
	}
	if err = sampleRows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		r := &out[i]
		r.Tags = []string{}
		for tag := range tagCounts[r.ArtistID] {
			r.Tags = append(r.Tags, tag)
		}
		sort.Slice(r.Tags, func(i, j int) bool {
			a, b := r.Tags[i], r.Tags[j]
			if tagCounts[r.ArtistID][a] != tagCounts[r.ArtistID][b] {
				return tagCounts[r.ArtistID][a] > tagCounts[r.ArtistID][b]
			}
			return a < b
		})
		if len(r.Tags) > 8 {
			r.Tags = r.Tags[:8]
		}
		r.UnknownCount += unknownSampleCount[r.ArtistID]
		r.UnknownAICount += unknownSampleCount[r.ArtistID]
		missing := len(r.SampleIDs) - resolved[r.ArtistID]
		if missing > 0 {
			r.UnknownCount += missing
			r.UnknownAICount += missing
		}
		switch {
		case r.AdultCount > 0:
			r.ContentRating = "adult"
		case !invalidSamples[r.ArtistID] && len(r.SampleIDs) > 0 && r.SafeCount == len(r.SampleIDs):
			r.ContentRating = "safe"
		default:
			r.ContentRating = "unknown"
		}
		switch {
		case r.AICount > 0:
			r.AIStatus = "ai"
		case !invalidSamples[r.ArtistID] && len(r.SampleIDs) > 0 && r.NonAICount == len(r.SampleIDs):
			r.AIStatus = "non_ai"
		default:
			r.AIStatus = "unknown"
		}
	}
	return out, nil
}

func parseRecommendationSampleIDs(raw string) ([]string, bool, int) {
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, true, 1
	}
	invalid := len(values) > 4
	unknownCount := 0
	if len(values) > 4 {
		unknownCount = len(values) - 4
	}
	if len(values) > 4 {
		values = values[:4]
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		var idText string
		if len(value) > 0 && value[0] == '"' {
			if err := json.Unmarshal(value, &idText); err != nil {
				invalid = true
				unknownCount++
				continue
			}
		} else {
			idText = string(value)
		}
		id, err := parseRecommendationSampleID(idText)
		if err != nil {
			invalid = true
			unknownCount++
			continue
		}
		canonical := strconv.FormatInt(id, 10)
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		ids = append(ids, canonical)
	}
	return ids, invalid, unknownCount
}

func parseRecommendationSampleID(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("empty sample ID")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid sample ID")
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid sample ID")
	}
	return id, nil
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
	var payload struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.RunID == "" {
		return domain.ErrLease
	}
	if err = checkRecommendationLease(ctx, tx, j, payload.RunID); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT
		(((a.review_status='approved' AND a.collection_enabled=1) OR (a.review_status='pending' AND a.remote_followed=1)) OR
		 (a.review_status='pending' AND EXISTS (
			SELECT 1 FROM recommendations r JOIN recommendation_runs rr ON rr.id=r.run_id
			WHERE r.run_id=? AND r.artist_id=a.id AND rr.state='running'
		 ))) AND a.review_status NOT IN ('blocked','rejected')
		FROM artists a WHERE a.id=?`, payload.RunID, artistID).Scan(&allowed); err != nil {
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
	if err = selectRecommendationExploration(ctx, tx, runID); err != nil {
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
	var handledCarryover int64
	err = tx.QueryRowContext(ctx, `
		SELECT r.artist_id FROM recommendations r
		JOIN artists a ON a.id=r.artist_id
		WHERE r.run_id=? AND COALESCE(json_extract(r.components_json,'$.carryover'),0)=1
		AND a.review_status!='pending' ORDER BY r.artist_id LIMIT 1`, runID).Scan(&handledCarryover)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if err = scheduleRecommendationRefill(ctx, tx, handledCarryover); err != nil {
			return err
		}
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
