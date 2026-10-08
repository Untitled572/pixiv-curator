package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"time"
)

// DecideArtist is the only path from pending to approved. Approval and task
// creation commit together. Blocked artists must first be explicitly restored.
func (s *Store) DecideArtist(ctx context.Context, id int64, action string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var state string
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT review_status,collection_enabled FROM artists WHERE id=?`, id).Scan(&state, &enabled)
	if err == sql.ErrNoRows {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	next := state
	jobID := ""
	switch action {
	case "approve":
		if state != "pending" && state != "approved" {
			return "", domain.ErrConflict
		}
		next = "approved"
		if state != "approved" || !enabled {
			enabled = true
			payload, _ := json.Marshal(map[string]any{"artist_id": id, "phase": "full"})
			jobID, err = enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist:%d", id), payload)
			if err != nil {
				return "", err
			}
		}
	case "reject":
		if state != "pending" && state != "rejected" {
			return "", domain.ErrConflict
		}
		next = "rejected"
		enabled = false
	case "block":
		next = "blocked"
		enabled = false
	case "restore":
		if state != "rejected" && state != "blocked" {
			return "", domain.ErrConflict
		}
		next = "pending"
		enabled = false
	default:
		return "", domain.ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE artists SET review_status=?,collection_enabled=?,updated_at=? WHERE id=?`, next, enabled, time.Now().UnixMilli(), id)
	if err != nil {
		return "", err
	}
	if !enabled {
		if err = cancelArtistJobs(ctx, tx, id); err != nil {
			return "", err
		}
	}
	return jobID, tx.Commit()
}

func cancelArtistJobs(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE kind='sync_artist' AND dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth')`, time.Now().UnixMilli(), fmt.Sprintf("artist:%d", id))
	return err
}

func (s *Store) SetCollection(ctx context.Context, id int64, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE artists SET collection_enabled=?,updated_at=? WHERE id=? AND review_status='approved'`, enabled, time.Now().UnixMilli(), id)
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
	if !enabled {
		err = cancelArtistJobs(ctx, tx, id)
	} else {
		payload, _ := json.Marshal(map[string]any{"artist_id": id, "phase": "full"})
		_, err = enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist:%d", id), payload)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// CommitArtistBatch validates the live lease and current user decision before
// storing a batch and its cursor. No network request is made within the transaction.
func (s *Store) CommitArtistBatch(ctx context.Context, j domain.Job, artistID int64, runID, phase, cursor string, artworks []domain.Artwork) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkLease(ctx, tx, j); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT review_status='approved' AND collection_enabled=1 FROM artists WHERE id=?`, artistID).Scan(&allowed); err != nil {
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
		if a.MetadataState != "complete" || a.Rating == "unknown" || a.AIStatus == "unknown" {
			payload, _ := json.Marshal(map[string]any{"illust_id": a.ID})
			if _, err = enqueue(ctx, tx, "hydrate_artwork", fmt.Sprintf("hydrate:%d", a.ID), payload); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artist_sync_state(artist_id,phase,cursor,run_id,last_full_sync_at,last_incremental_at) VALUES(?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET phase=excluded.phase,cursor=excluded.cursor,run_id=excluded.run_id,last_full_sync_at=COALESCE(excluded.last_full_sync_at,artist_sync_state.last_full_sync_at),last_incremental_at=COALESCE(excluded.last_incremental_at,artist_sync_state.last_incremental_at)`, artistID, phase, cursor, runID, finishedAt(cursor, phase == "full"), finishedAt(cursor, phase == "incremental"))
	if err != nil {
		return err
	}
	if cursor == "" {
		_, err = tx.ExecContext(ctx, `UPDATE artists SET last_synced_at=?,updated_at=? WHERE id=?`, time.Now().UnixMilli(), time.Now().UnixMilli(), artistID)
		if err != nil {
			return err
		}
		if err = completeJob(ctx, tx, j); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func finishedAt(cursor string, phase bool) any {
	if cursor == "" && phase {
		return time.Now().UnixMilli()
	}
	return nil
}

func checkLease(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT state='running' AND lease_token=? AND lease_until>? AND cancel_requested=0 FROM jobs WHERE id=?`, j.LeaseToken, time.Now().UnixMilli(), j.ID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrLease
	}
	return nil
}

// CommitFollowingBatch marks unfollowed artists only on a successfully completed
// snapshot. Local review and collection state are never changed by following.
func (s *Store) CommitFollowingBatch(ctx context.Context, j domain.Job, runID string, artists []domain.Artist, nextPayload json.RawMessage, complete bool) error {
	if runID == "" || !json.Valid(nextPayload) {
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
	for _, a := range artists {
		if a.ID <= 0 {
			return domain.ErrInvalid
		}
		if err = upsertArtist(ctx, tx, a, "following", "account"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=1,last_seen_follow_run=? WHERE id=?`, runID, a.ID); err != nil {
			return err
		}
	}
	if complete {
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=0,updated_at=? WHERE remote_followed=1 AND last_seen_follow_run<>?`, time.Now().UnixMilli(), runID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET payload_json=?,updated_at=? WHERE id=?`, string(nextPayload), time.Now().UnixMilli(), j.ID)
	if err != nil {
		return err
	}
	if complete {
		if err = completeJob(ctx, tx, j); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func completeJob(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	r, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',lease_token='',lease_until=0,updated_at=? WHERE id=? AND lease_token=? AND lease_until>? AND cancel_requested=0`, time.Now().UnixMilli(), j.ID, j.LeaseToken, time.Now().UnixMilli())
	return leaseResult(r, err)
}

func (s *Store) ArtistCursor(ctx context.Context, id int64, runID string) (string, error) {
	var cursor string
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM artist_sync_state WHERE artist_id=? AND run_id=?`, id, runID).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cursor, err
}

func (s *Store) CommitHydration(ctx context.Context, j domain.Job, a domain.Artwork) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkLease(ctx, tx, j); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT (review_status='approved' AND collection_enabled=1) OR (?='repair_media' AND review_status IN ('approved','pending')) FROM artists WHERE id=?`, j.Kind, a.ArtistID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
	}
	if err = upsertArtwork(ctx, tx, a); err != nil {
		return err
	}
	if err = completeJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitUnavailable(ctx context.Context, j domain.Job, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkLease(ctx, tx, j); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE artworks SET metadata_state='unavailable',updated_at=? WHERE id=?`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE image_pages SET media_state='unavailable',last_error_code='NOT_FOUND',updated_at=? WHERE illust_id=?`, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if err = completeJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResumeJob(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='pending',attempt=0,available_at=?,error_code='',updated_at=? WHERE id=? AND state='paused_auth' AND cancel_requested=0`, time.Now().UnixMilli(), time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return domain.ErrConflict
	}
	return err
}
