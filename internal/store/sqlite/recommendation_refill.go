package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const recommendationRefillDelay = 5 * time.Minute

type recommendationRefillPayload struct {
	RunID string `json:"run_id"`
}

// scheduleRecommendationRefill debounces an idle-pool refill after an explicit
// decision on a candidate from the latest successful recommendation run.
func scheduleRecommendationRefill(ctx context.Context, tx *sql.Tx, artistID int64) error {
	var runID string
	err := tx.QueryRowContext(ctx, `
		SELECT rr.id FROM recommendation_runs rr
		JOIN recommendations r ON r.run_id=rr.id
		WHERE rr.id=(SELECT id FROM recommendation_runs ORDER BY started_at DESC,rowid DESC LIMIT 1)
		AND rr.state='succeeded' AND r.artist_id=?`, artistID).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='pending'`, runID).Scan(&pending); err != nil {
		return err
	}
	if pending >= 30 {
		return nil
	}

	now := time.Now()
	due := now.Add(recommendationRefillDelay).UnixMilli()
	payload, _ := json.Marshal(recommendationRefillPayload{RunID: runID})
	const key = "recommendation-refill"
	var activeID, state string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM jobs WHERE dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth')`, key).Scan(&activeID, &state)
	if err == nil {
		if state == "running" {
			if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='canceled',cancel_requested=0,lease_token='',lease_until=0,error_code='',updated_at=? WHERE id=? AND state='running'`, now.UnixMilli(), activeID); err != nil {
				return err
			}
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET payload_json=?,available_at=?,attempt=0,cancel_requested=0,error_code='',updated_at=? WHERE id=? AND state IN ('pending','retry_wait','paused_auth')`, string(payload), due, now.UnixMilli(), activeID)
			return err
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES(?, 'recommendation_refill', ?, ?, 'pending', ?, ?, ?)`, randomID(), key, string(payload), due, now.UnixMilli(), now.UnixMilli())
	return err
}

// PrepareRecommendationRefill restores a delayed coordinator after process
// restart without resetting an existing debounce deadline.
func (s *Store) PrepareRecommendationRefill(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE dedupe_key='recommendation-refill' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return tx.Commit()
	}
	var runID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM recommendation_runs WHERE state='succeeded' ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&runID)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='pending'`, runID).Scan(&pending); err != nil {
		return err
	}
	if pending >= 30 {
		return tx.Commit()
	}
	var artistID int64
	err = tx.QueryRowContext(ctx, `SELECT artist_id FROM recommendations WHERE run_id=? ORDER BY artist_id LIMIT 1`, runID).Scan(&artistID)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err = scheduleRecommendationRefill(ctx, tx, artistID); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchRecommendationActivity pushes an existing idle-refill deadline forward.
// If its coordinator is already running, revoke that lease and replace it.
func (s *Store) TouchRecommendationActivity(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	due := now.Add(recommendationRefillDelay).UnixMilli()
	var id, state string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM jobs WHERE dedupe_key='recommendation-refill' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&id, &state)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if state == "running" {
		var raw string
		if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM jobs WHERE id=?`, id).Scan(&raw); err != nil {
			return err
		}
		var p recommendationRefillPayload
		if json.Unmarshal([]byte(raw), &p) != nil || p.RunID == "" {
			return domain.ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='canceled',cancel_requested=0,lease_token='',lease_until=0,error_code='',updated_at=? WHERE id=? AND state='running'`, now.UnixMilli(), id); err != nil {
			return err
		}
		encoded, _ := json.Marshal(p)
		if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES(?, 'recommendation_refill', 'recommendation-refill', ?, 'pending', ?, ?, ?)`, randomID(), string(encoded), due, now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET available_at=?,updated_at=? WHERE id=? AND state IN ('pending','retry_wait','paused_auth')`, due, now.UnixMilli(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StartRecommendationRefill carries untouched candidates to a fresh run, then
// queues ordinary recommendation work only for the available slots.
func (s *Store) StartRecommendationRefill(ctx context.Context, j domain.Job, budget json.RawMessage) error {
	if j.Kind != "recommendation_refill" || len(budget) == 0 || !json.Valid(budget) {
		return domain.ErrInvalid
	}
	var payload recommendationRefillPayload
	if json.Unmarshal(j.Payload, &payload) != nil || payload.RunID == "" {
		return domain.ErrLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkLease(ctx, tx, j); err != nil {
		return err
	}
	var latestID, state string
	var latestStarted int64
	err = tx.QueryRowContext(ctx, `SELECT id,state,started_at FROM recommendation_runs ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&latestID, &state, &latestStarted)
	if err == sql.ErrNoRows {
		return completeRefillNoop(ctx, tx, j)
	}
	if err != nil {
		return err
	}
	if latestID != payload.RunID || state != "succeeded" {
		return completeRefillNoop(ctx, tx, j)
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM recommendations r JOIN artists a ON a.id=r.artist_id WHERE r.run_id=? AND a.review_status='pending'`, latestID).Scan(&pending); err != nil {
		return err
	}
	if pending > 30 {
		pending = 30
	}
	missing := 30 - pending
	if missing == 0 {
		return completeRefillNoop(ctx, tx, j)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind='recommend_artists' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return completeRefillNoop(ctx, tx, j)
	}

	var fields map[string]json.RawMessage
	if err = json.Unmarshal(budget, &fields); err != nil {
		return domain.ErrInvalid
	}
	parent, _ := json.Marshal(latestID)
	fields["refill_parent_run"] = parent
	setJSONInt(fields, "candidate_artists", missing)
	setJSONInt(fields, "recall_artists", min(60, pending+2*missing))
	setJSONInt(fields, "exploration_artists", missing/5)
	budget, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	budget, err = freezeRecommendationExclusions(ctx, tx, budget, latestID)
	if err != nil {
		return err
	}
	budget, err = freezeRecommendationSeeds(ctx, tx, budget)
	if err != nil {
		return err
	}
	newRunID := randomID()
	now := time.Now().UnixMilli()
	if now <= latestStarted {
		now = latestStarted + 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO recommendation_runs(id,state,input_version,budget_json,started_at) VALUES(?,'running','v1',?,?)`, newRunID, string(budget), now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at)
		SELECT ?,r.artist_id,r.score,r.reasons_json,r.sample_illust_ids_json,r.sources_json,r.confidence,r.score_version,
			json_set(r.components_json,'$.carryover',json('true')),?,?
		FROM recommendations r JOIN artists a ON a.id=r.artist_id
		WHERE r.run_id=? AND a.review_status='pending'
		ORDER BY r.score DESC,r.artist_id LIMIT 30`, newRunID, now, now, latestID); err != nil {
		return err
	}
	workPayload, _ := json.Marshal(map[string]string{"run_id": newRunID})
	if _, err = enqueue(ctx, tx, "recommend_artists", "recommend:"+newRunID, workPayload); err != nil {
		return err
	}
	return completeRefillNoop(ctx, tx, j)
}

func completeRefillNoop(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	if err := completeJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func setJSONInt(fields map[string]json.RawMessage, key string, value int) {
	encoded, _ := json.Marshal(value)
	fields[key] = encoded
}
