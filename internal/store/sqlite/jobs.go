package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"time"
)

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) Enqueue(ctx context.Context, kind, key string, payload json.RawMessage) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := enqueue(ctx, tx, kind, key, payload)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
func enqueue(ctx context.Context, tx *sql.Tx, kind, key string, payload json.RawMessage) (string, error) {
	if key == "" || !json.Valid(payload) {
		return "", domain.ErrInvalid
	}
	switch kind {
	case "sync_following", "sync_artist", "hydrate_artwork", "recommend_artists", "repair_media", "follow_artist":
	default:
		return "", domain.ErrInvalid
	}
	id := randomID()
	now := time.Now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?,?) ON CONFLICT DO NOTHING`, id, kind, key, string(payload), now, now, now)
	if err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth')`, key).Scan(&id)
	return id, err
}

// Claim recovers expired leases and grants one new lease in the same transaction.
func (s *Store) Claim(ctx context.Context, now time.Time) (domain.Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state=CASE WHEN cancel_requested=1 THEN 'canceled' WHEN attempt>=5 THEN 'failed' ELSE 'retry_wait' END,lease_token='',lease_until=0,available_at=?,updated_at=?,error_code=CASE WHEN cancel_requested=1 THEN '' ELSE 'LEASE_EXPIRED' END WHERE state='running' AND lease_until<=?`, now.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return domain.Job{}, err
	}
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE state IN ('pending','retry_wait') AND available_at<=? AND cancel_requested=0 ORDER BY CASE WHEN kind='sync_following' THEN 0 WHEN kind='follow_artist' THEN 1 WHEN kind IN ('recommend_artists','recommendation_refill') THEN 2 WHEN json_extract(payload_json,'$.phase')='history' THEN 4 ELSE 3 END,available_at,id LIMIT 1`, now.UnixMilli()))
	if err != nil {
		if err == domain.ErrNotFound {
			if commitErr := tx.Commit(); commitErr != nil {
				return j, commitErr
			}
		}
		return j, err
	}
	j.State = "running"
	j.Attempt++
	j.LeaseToken = randomID()
	j.LeaseUntil = now.Add(90 * time.Second).UnixMilli()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='running',attempt=?,lease_token=?,lease_until=?,updated_at=? WHERE id=?`, j.Attempt, j.LeaseToken, j.LeaseUntil, now.UnixMilli(), j.ID)
	if err != nil {
		return domain.Job{}, err
	}
	return j, tx.Commit()
}

const jobColumns = `id,kind,dedupe_key,payload_json,state,attempt,available_at,lease_token,lease_until,cancel_requested,error_code,progress_pages,progress_items,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (domain.Job, error) {
	var j domain.Job
	var raw string
	err := row.Scan(&j.ID, &j.Kind, &j.Key, &raw, &j.State, &j.Attempt, &j.AvailableAt, &j.LeaseToken, &j.LeaseUntil, &j.CancelRequested, &j.ErrorCode, &j.ProgressPages, &j.ProgressItems, &j.CreatedAt, &j.UpdatedAt)
	j.Payload = json.RawMessage(raw)
	if err == sql.ErrNoRows {
		err = domain.ErrNotFound
	}
	return j, err
}

func (s *Store) Renew(ctx context.Context, j domain.Job, now time.Time) error {
	// A canceled lease is revoked and finalized atomically. Returning ErrLease
	// tells the worker to stop its handler; stale writes remain fenced out.
	var canceled bool
	err := s.db.QueryRowContext(ctx, `UPDATE jobs SET
 state=CASE WHEN cancel_requested=1 THEN 'canceled' ELSE state END,
 lease_token=CASE WHEN cancel_requested=1 THEN '' ELSE lease_token END,
 lease_until=CASE WHEN cancel_requested=1 THEN 0 ELSE ? END,
 error_code=CASE WHEN cancel_requested=1 THEN '' ELSE error_code END,
 updated_at=? WHERE id=? AND state='running' AND lease_token=? AND lease_until>?
 RETURNING cancel_requested`, now.Add(90*time.Second).UnixMilli(), now.UnixMilli(), j.ID, j.LeaseToken, now.UnixMilli()).Scan(&canceled)
	if err == sql.ErrNoRows || err == nil && canceled {
		return domain.ErrLease
	}
	return err
}

func leaseResult(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrLease
	}
	return nil
}

// Finish fences expired workers. Stable codes are supplied by trusted handlers,
// never by upstream response bodies. Cancellation wins over success and retries.
func (s *Store) Finish(ctx context.Context, j domain.Job, state, code string, available time.Time) error {
	switch state {
	case "succeeded", "retry_wait", "deferred", "failed", "paused_auth", "canceled":
	default:
		return domain.ErrInvalid
	}
	deferred := state == "deferred"
	if deferred {
		// This is an internal completion command, never a persisted job state.
		state = "retry_wait"
	} else if state == "retry_wait" && j.Attempt >= 5 {
		state = "failed"
	}
	r, err := s.db.ExecContext(ctx, `UPDATE jobs SET state=CASE WHEN cancel_requested=1 THEN 'canceled' ELSE ? END,error_code=CASE WHEN cancel_requested=1 OR ?='canceled' THEN '' ELSE ? END,attempt=CASE WHEN ? AND cancel_requested=0 AND attempt>0 THEN attempt-1 ELSE attempt END,available_at=?,lease_token='',lease_until=0,updated_at=? WHERE id=? AND state='running' AND lease_token=? AND lease_until>?`, state, state, code, deferred, available.UnixMilli(), time.Now().UnixMilli(), j.ID, j.LeaseToken, time.Now().UnixMilli())
	return leaseResult(r, err)
}

func (s *Store) Cancel(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE id=? AND state IN ('pending','running','retry_wait','paused_auth')`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return domain.ErrConflict
	}
	return err
}

func (s *Store) Jobs(ctx context.Context, after string, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 200 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *Store) RetryJob(ctx context.Context, id string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
	if err != nil {
		return "", err
	}
	if j.State != "failed" && j.State != "canceled" && j.State != "succeeded" {
		return "", domain.ErrConflict
	}
	if j.Kind == "sync_following" {
		var payload map[string]any
		if json.Unmarshal(j.Payload, &payload) != nil {
			return "", domain.ErrInvalid
		}
		payload["cursor"] = ""
		j.Payload, _ = json.Marshal(payload)
	}
	if j.Kind == "sync_artist" {
		var p struct {
			ID int64 `json:"artist_id"`
		}
		if json.Unmarshal(j.Payload, &p) != nil {
			return "", domain.ErrInvalid
		}
		var allowed bool
		if err = tx.QueryRowContext(ctx, `SELECT review_status='approved' AND collection_enabled=1 FROM artists WHERE id=?`, p.ID).Scan(&allowed); err != nil {
			return "", err
		}
		if !allowed {
			return "", domain.ErrConflict
		}
	}
	newID, err := enqueue(ctx, tx, j.Kind, j.Key, j.Payload)
	if err != nil {
		return "", err
	}
	return newID, tx.Commit()
}

func (s *Store) Job(ctx context.Context, id string) (domain.Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
}
