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

// Automatic replenishment is disabled. Decisions only remove handled cards.
func scheduleRecommendationRefill(context.Context, *sql.Tx, int64) error { return nil }

// Revoke old coordinators and the recommendation jobs they already created.
func (s *Store) PrepareRecommendationRefill(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='canceled',lease_token='',lease_until=0,cancel_requested=0,updated_at=? WHERE state IN ('pending','running','retry_wait','paused_auth') AND (kind='recommendation_refill' OR (kind='recommend_artists' AND json_extract(payload_json,'$.run_id') IN (SELECT id FROM recommendation_runs WHERE json_type(budget_json,'$.refill_parent_run') IS NOT NULL)))`, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET state='canceled',finished_at=? WHERE state='running' AND json_type(budget_json,'$.refill_parent_run') IS NOT NULL`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TouchRecommendationActivity(context.Context) error { return nil }

func (s *Store) StartRecommendationRefill(ctx context.Context, j domain.Job, budget json.RawMessage) error {
	if j.Kind != "recommendation_refill" {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkLease(ctx, tx, j); err != nil {
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
