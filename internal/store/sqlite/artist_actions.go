package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
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
	case "approve", "unblock":
		if action != "unblock" && state != "pending" && state != "approved" {
			return "", domain.ErrConflict
		}
		next = "approved"
		if state != "approved" || !enabled {
			enabled = true
			payload, _ := json.Marshal(map[string]any{"artist_id": id, "phase": "recent"})
			jobID, err = enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist:%d", id), payload)
			if err != nil {
				return "", err
			}
		}
	case "reject", "remove":
		if action == "remove" && state != "approved" && state != "rejected" {
			return "", domain.ErrConflict
		}
		if state != "pending" && state != "rejected" {
			if action != "remove" || state != "approved" {
				return "", domain.ErrConflict
			}
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
	if action == "approve" && state == "pending" {
		var accountID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE deleted=0 AND is_default=1 AND follow_on_approve=1 AND EXISTS(SELECT 1 FROM artist_sources WHERE artist_id=? AND source_type='recommend')`, id).Scan(&accountID)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if err == nil {
			if _, err = enqueueRemoteFollow(ctx, tx, accountID, id, "approval", "public"); err != nil {
				return "", err
			}
		}
	}
	if next == "blocked" || next == "rejected" {
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE kind='follow_artist' AND json_extract(payload_json,'$.artist_id')=? AND state IN ('pending','running','retry_wait','paused_auth')`, time.Now().UnixMilli(), id); err != nil {
			return "", err
		}
	}
	if !enabled {
		if err = cancelArtistJobs(ctx, tx, id); err != nil {
			return "", err
		}
		if action == "block" || action == "reject" || action == "remove" {
			if err = cancelRemoteFollowJobs(ctx, tx, id); err != nil {
				return "", err
			}
		}
	}
	if action == "approve" || action == "reject" {
		if err = scheduleRecommendationRefill(ctx, tx, id); err != nil {
			return "", err
		}
	}
	return jobID, tx.Commit()
}

func cancelArtistJobs(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE state IN ('pending','running','retry_wait','paused_auth') AND ((kind='sync_artist' AND json_extract(payload_json,'$.artist_id')=?) OR (kind IN ('hydrate_artwork','repair_media') AND EXISTS (SELECT 1 FROM artworks w WHERE w.id=json_extract(jobs.payload_json,'$.illust_id') AND w.artist_id=?)))`, time.Now().UnixMilli(), id, id)
	return err
}

// ValidateCollectionJob prevents paused artists from starting new upstream fetches.
// The commit methods repeat this check to fence requests already in flight.
func (s *Store) ValidateCollectionJob(ctx context.Context, j domain.Job) error {
	var allowed bool
	var query string
	switch j.Kind {
	case "sync_artist":
		query = `SELECT a.review_status='approved' AND a.collection_enabled=1 FROM artists a WHERE a.id=json_extract(?, '$.artist_id')`
	case "hydrate_artwork", "repair_media":
		query = `SELECT (a.review_status='approved' AND a.collection_enabled=1) OR (?='repair_media' AND a.review_status='pending') FROM artists a JOIN artworks w ON w.artist_id=a.id WHERE w.id=json_extract(?, '$.illust_id')`
	default:
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT state='running' AND lease_token=? AND lease_until>? AND cancel_requested=0 FROM jobs WHERE id=?`, j.LeaseToken, time.Now().UnixMilli(), j.ID).Scan(&live); err != nil {
		return err
	}
	if !live {
		return domain.ErrLease
	}
	if j.Kind == "sync_artist" {
		err = tx.QueryRowContext(ctx, query, j.Payload).Scan(&allowed)
	} else {
		err = tx.QueryRowContext(ctx, query, j.Kind, j.Payload).Scan(&allowed)
	}
	if err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
	}
	return nil
}

func cancelRemoteFollowJobs(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE kind='follow_artist' AND json_extract(payload_json,'$.artist_id')=? AND state IN ('pending','running','retry_wait','paused_auth')`, time.Now().UnixMilli(), id)
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
		payload, _ := json.Marshal(map[string]any{"artist_id": id, "phase": "recent"})
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
	// A committed cursor is the page fence: if the same page is retried after
	// its transaction committed, do not count or persist it twice.
	var savedPhase, savedCursor, savedRun string
	err = tx.QueryRowContext(ctx, `SELECT phase,cursor,run_id FROM artist_sync_state WHERE artist_id=?`, artistID).Scan(&savedPhase, &savedCursor, &savedRun)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && savedPhase == phase && savedCursor == cursor && savedRun == runID {
		return tx.Commit()
	}
	for _, a := range artworks {
		if a.ArtistID != artistID {
			return domain.ErrInvalid
		}
		if err = upsertArtwork(ctx, tx, a); err != nil {
			return err
		}
		if a.MetadataState != "complete" || a.Rating == "unknown" || a.AIStatus == "unknown" {
			payload, _ := json.Marshal(map[string]any{"illust_id": a.ID, "account_id": domain.ContextAccount(ctx)})
			if _, err = enqueue(ctx, tx, "hydrate_artwork", fmt.Sprintf("hydrate:%d", a.ID), payload); err != nil {
				return err
			}
		}
	}
	if err = incrementJobProgress(ctx, tx, j, len(artworks)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artist_sync_state(artist_id,phase,cursor,run_id,last_full_sync_at,last_incremental_at) VALUES(?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET phase=excluded.phase,cursor=excluded.cursor,run_id=excluded.run_id,last_full_sync_at=COALESCE(excluded.last_full_sync_at,artist_sync_state.last_full_sync_at),last_incremental_at=COALESCE(excluded.last_incremental_at,artist_sync_state.last_incremental_at)`, artistID, phase, cursor, runID, finishedAt(cursor, phase == "full" || phase == "history" || phase == "recent"), finishedAt(cursor, phase == "incremental"))
	if err != nil {
		return err
	}
	var progress int64
	if err = tx.QueryRowContext(ctx, `SELECT progress_items FROM jobs WHERE id=?`, j.ID).Scan(&progress); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET payload_json=json_set(payload_json,'$.cursor',?) WHERE id=?`, cursor, j.ID); err != nil {
		return err
	}
	stopRecent := (phase == "recent" || phase == "incremental") && progress >= 100
	if stopRecent && cursor != "" {
		payload, _ := json.Marshal(map[string]any{"artist_id": artistID, "phase": "history", "cursor": cursor, "account_id": domain.ContextAccount(ctx)})
		if _, err = enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist-history:%d", artistID), payload); err != nil {
			return err
		}
	}
	if cursor == "" || stopRecent {
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
// snapshot. Following enrolls artists automatically, preserving explicit exclusions.
func (s *Store) CommitFollowingBatch(ctx context.Context, j domain.Job, runID string, artists []domain.Artist, nextPayload json.RawMessage, complete bool) error {
	if runID == "" || !json.Valid(nextPayload) {
		return domain.ErrInvalid
	}
	visibility := domain.ContextFollowVisibility(ctx)
	if !domain.ValidFollowVisibility(visibility) {
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
	var savedPayload string
	var savedPages int64
	err = tx.QueryRowContext(ctx, `SELECT payload_json,progress_pages FROM jobs WHERE id=?`, j.ID).Scan(&savedPayload, &savedPages)
	if err != nil {
		return err
	}
	if savedPages > 0 && savedPayload == string(nextPayload) {
		return tx.Commit()
	}
	accountID := domain.ContextAccount(ctx)
	if accountID == "" {
		var routing struct {
			ID string `json:"account_id"`
		}
		_ = json.Unmarshal(nextPayload, &routing)
		accountID = routing.ID
	}
	for _, a := range artists {
		if a.ID <= 0 {
			return domain.ErrInvalid
		}
		if err = upsertArtist(ctx, tx, a, "following", accountID); err != nil {
			return err
		}
		if accountID != "" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,last_seen_run,visibility) VALUES(?,?,1,?,?) ON CONFLICT(account_id,artist_id) DO UPDATE SET followed=1,last_seen_run=excluded.last_seen_run,visibility=excluded.visibility`, accountID, a.ID, runID, visibility); err != nil {
				return err
			}
		}
		// The revised workflow treats following as enrollment, but never overrides
		// an explicit rejection, block, or paused collection.
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=? AND review_status='pending'`, a.ID); err != nil {
			return err
		}
		var enabled bool
		if err = tx.QueryRowContext(ctx, `SELECT review_status='approved' AND collection_enabled=1 FROM artists WHERE id=?`, a.ID).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			phase := "recent"
			var indexed bool
			if err = tx.QueryRowContext(ctx, `SELECT last_synced_at IS NOT NULL FROM artists WHERE id=?`, a.ID).Scan(&indexed); err != nil {
				return err
			}
			if indexed {
				phase = "incremental"
			}
			payload, _ := json.Marshal(map[string]any{"artist_id": a.ID, "phase": phase, "account_id": accountID})
			if _, err = enqueue(ctx, tx, "sync_artist", fmt.Sprintf("artist:%d", a.ID), payload); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=1,last_seen_follow_run=? WHERE id=?`, runID, a.ID); err != nil {
			return err
		}
	}
	if complete && visibility == "private" {
		if accountID != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE account_artists SET followed=0 WHERE account_id=? AND last_seen_run<>?`, accountID, runID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=artists.id AND aa.followed=1 AND ac.deleted=0),updated_at=?`, time.Now().UnixMilli()); err != nil {
				return err
			}
		}
	} else if complete && accountID == "" {
		if _, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=0,updated_at=? WHERE remote_followed=1 AND last_seen_follow_run<>?`, time.Now().UnixMilli(), runID); err != nil {
			return err
		}
	}
	if err = incrementJobProgress(ctx, tx, j, len(artists)); err != nil {
		return err
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

func incrementJobProgress(ctx context.Context, tx *sql.Tx, j domain.Job, items int) error {
	r, err := tx.ExecContext(ctx, `UPDATE jobs SET progress_pages=progress_pages+1,progress_items=progress_items+?,updated_at=? WHERE id=? AND state='running' AND lease_token=? AND lease_until>? AND cancel_requested=0`, items, time.Now().UnixMilli(), j.ID, j.LeaseToken, time.Now().UnixMilli())
	return leaseResult(r, err)
}

func completeJob(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	r, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',error_code='',lease_token='',lease_until=0,updated_at=? WHERE id=? AND lease_token=? AND lease_until>? AND cancel_requested=0`, time.Now().UnixMilli(), j.ID, j.LeaseToken, time.Now().UnixMilli())
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
	if err = tx.QueryRowContext(ctx, `SELECT (review_status='approved' AND collection_enabled=1) OR (?='repair_media' AND review_status='pending') FROM artists WHERE id=?`, j.Kind, a.ArtistID).Scan(&allowed); err != nil {
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
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT (a.review_status='approved' AND a.collection_enabled=1) OR (?='repair_media' AND a.review_status='pending') FROM artworks w JOIN artists a ON a.id=w.artist_id WHERE w.id=?`, j.Kind, id).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return domain.ErrConflict
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
