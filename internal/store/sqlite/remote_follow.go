package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type remoteFollowPayload struct {
	AccountID string `json:"account_id"`
	ArtistID  int64  `json:"artist_id"`
	Origin    string `json:"origin"`
	Restrict  string `json:"restrict,omitempty"`
}

func validRemoteFollow(accountID string, artistID int64, origin string) bool {
	return accountID != "" && strings.TrimSpace(accountID) == accountID && artistID > 0 && (origin == "copy" || origin == "approval")
}

func remoteFollowJobPayload(j domain.Job, accountID string, artistID int64, origin string) (string, error) {
	if !validRemoteFollow(accountID, artistID, origin) || j.Kind != "follow_artist" || j.ID == "" || j.LeaseToken == "" {
		return "", domain.ErrInvalid
	}
	var payload remoteFollowPayload
	if err := json.Unmarshal(j.Payload, &payload); err != nil {
		return "", domain.ErrInvalid
	}
	if payload.AccountID != accountID || payload.ArtistID != artistID || payload.Origin != origin {
		return "", domain.ErrInvalid
	}
	restrict := payload.Restrict
	if restrict == "" {
		restrict = "public"
	}
	if !domain.ValidFollowVisibility(restrict) || origin == "approval" && restrict != "public" {
		return "", domain.ErrInvalid
	}
	return restrict, nil
}

func remoteFollowKey(accountID string, artistID int64) string {
	return "follow:" + accountID + ":" + strconv.FormatInt(artistID, 10)
}

func enqueueRemoteFollow(ctx context.Context, tx *sql.Tx, accountID string, artistID int64, origin, restrict string) (bool, error) {
	if !validRemoteFollow(accountID, artistID, origin) || !domain.ValidFollowVisibility(restrict) || origin == "approval" && restrict != "public" {
		return false, domain.ErrInvalid
	}
	key := remoteFollowKey(accountID, artistID)
	payload, err := json.Marshal(remoteFollowPayload{AccountID: accountID, ArtistID: artistID, Origin: origin, Restrict: restrict})
	if err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	var activeID, activeKind, activePayload string
	err = tx.QueryRowContext(ctx, `SELECT id,kind,payload_json FROM jobs WHERE dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth')`, key).Scan(&activeID, &activeKind, &activePayload)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if err == nil {
		if activeKind != "follow_artist" {
			return false, domain.ErrConflict
		}
		var active remoteFollowPayload
		if json.Unmarshal([]byte(activePayload), &active) == nil {
			activeRestrict := active.Restrict
			if activeRestrict == "" {
				activeRestrict = "public"
			}
			if activeRestrict == restrict && !(active.Origin == "approval" && origin == "copy") {
				return false, nil
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE jobs SET state='canceled',cancel_requested=1,lease_token='',lease_until=0,error_code='',updated_at=? WHERE id=?`, now, activeID); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES(?,'follow_artist',?,?,'pending',?,?,?)`, randomID(), key, string(payload), now, now, now)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// QueueFollowCopy queues one remote follow for each eligible source-account artist
// that is not already followed by the target account. It never approves artists.
func (s *Store) QueueFollowCopy(ctx context.Context, source, target string) (int, error) {
	if source == "" || target == "" || strings.TrimSpace(source) != source || strings.TrimSpace(target) != target || source == target {
		return 0, domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE deleted=0 AND id IN (?,?) AND pixiv_id>0`, source, target).Scan(&active); err != nil {
		return 0, err
	}
	if active != 2 {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE deleted=0 AND id IN (?,?)`, source, target).Scan(&exists); err != nil {
			return 0, err
		}
		if exists != 2 {
			return 0, domain.ErrNotFound
		}
		return 0, domain.ErrConflict
	}
	var synced bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE kind='sync_following' AND state='succeeded' AND json_extract(payload_json,'$.account_id')=? AND json_extract(payload_json,'$.restrict')='private')`, source).Scan(&synced); err != nil {
		return 0, err
	}
	if !synced {
		return 0, domain.ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT aa.artist_id,aa.visibility FROM account_artists aa JOIN artists a ON a.id=aa.artist_id
 WHERE aa.account_id=? AND aa.followed=1 AND a.review_status NOT IN ('blocked','rejected')
 AND NOT EXISTS(SELECT 1 FROM account_artists target WHERE target.account_id=? AND target.artist_id=aa.artist_id AND target.followed=1 AND target.visibility=aa.visibility)
 ORDER BY aa.artist_id`, source, target)
	if err != nil {
		return 0, err
	}
	type copyTarget struct {
		artistID int64
		restrict string
	}
	copyTargets := []copyTarget{}
	for rows.Next() {
		var item copyTarget
		if err = rows.Scan(&item.artistID, &item.restrict); err != nil {
			rows.Close()
			return 0, err
		}
		copyTargets = append(copyTargets, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	queued := 0
	for _, artist := range copyTargets {
		added, queueErr := enqueueRemoteFollow(ctx, tx, target, artist.artistID, "copy", artist.restrict)
		if queueErr != nil {
			return 0, queueErr
		}
		if added {
			queued++
		}
	}
	return queued, tx.Commit()
}

// NeedsRemoteFollow rechecks the lease and current account/artist policy before
// a worker makes a Pixiv request. It performs no network work.
func (s *Store) NeedsRemoteFollow(ctx context.Context, j domain.Job, accountID string, artistID int64, origin string) (bool, error) {
	restrict, err := remoteFollowJobPayload(j, accountID, artistID, origin)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = checkRemoteFollowLease(ctx, tx, j); err != nil {
		return false, err
	}
	var status string
	var followOnApprove, followed, recommended bool
	err = tx.QueryRowContext(ctx, `SELECT a.follow_on_approve,ar.review_status,
 EXISTS(SELECT 1 FROM account_artists aa WHERE aa.account_id=a.id AND aa.artist_id=ar.id AND aa.followed=1 AND aa.visibility=?)
 ,EXISTS(SELECT 1 FROM artist_sources src WHERE src.artist_id=ar.id AND src.source_type='recommend')
	FROM accounts a JOIN artists ar ON ar.id=? WHERE a.id=? AND a.deleted=0`, restrict, artistID, accountID).Scan(&followOnApprove, &status, &followed, &recommended)
	if err == sql.ErrNoRows {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if status == "blocked" || status == "rejected" || followed {
		return false, nil
	}
	if origin == "approval" && (!followOnApprove || status != "approved" || !recommended) {
		return false, nil
	}
	return true, nil
}

// CommitRemoteFollow commits the completed upstream action only while the job
// lease and current user policy remain valid.
func (s *Store) CommitRemoteFollow(ctx context.Context, j domain.Job, accountID string, artistID int64, origin string) error {
	restrict, err := remoteFollowJobPayload(j, accountID, artistID, origin)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkRemoteFollowLease(ctx, tx, j); err != nil {
		return err
	}
	var status string
	var followOnApprove, recommended bool
	err = tx.QueryRowContext(ctx, `SELECT ar.review_status,a.follow_on_approve,
 EXISTS(SELECT 1 FROM artist_sources src WHERE src.artist_id=ar.id AND src.source_type='recommend')
 FROM accounts a JOIN artists ar ON ar.id=? WHERE a.id=? AND a.deleted=0`, artistID, accountID).Scan(&status, &followOnApprove, &recommended)
	if err == sql.ErrNoRows {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "blocked" || status == "rejected" || origin == "approval" && (status != "approved" || !followOnApprove || !recommended) {
		return domain.ErrConflict
	}
	var alreadySameVisibility bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_artists WHERE account_id=? AND artist_id=? AND followed=1 AND visibility=?)`, accountID, artistID, restrict).Scan(&alreadySameVisibility); err != nil {
		return err
	}
	if alreadySameVisibility {
		if err = completeJob(ctx, tx, j); err != nil {
			return err
		}
		return tx.Commit()
	}
	now := time.Now().UnixMilli()
	followedAt := int64(0)
	if origin == "approval" {
		followedAt = now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,followed_at,visibility) VALUES(?,?,1,?,?) ON CONFLICT(account_id,artist_id) DO UPDATE SET followed=1,followed_at=CASE WHEN ?='approval' THEN excluded.followed_at ELSE account_artists.followed_at END,visibility=excluded.visibility`, accountID, artistID, followedAt, restrict, origin)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=artists.id AND aa.followed=1 AND ac.deleted=0),updated_at=? WHERE id=?`, now, artistID)
	if err != nil {
		return err
	}
	if err = completeJob(ctx, tx, j); err != nil {
		return err
	}
	return tx.Commit()
}

func checkRemoteFollowLease(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT kind='follow_artist' AND payload_json=? AND state='running' AND lease_token=? AND lease_until>? AND cancel_requested=0 FROM jobs WHERE id=?`, string(j.Payload), j.LeaseToken, time.Now().UnixMilli(), j.ID).Scan(&valid)
	if err == sql.ErrNoRows || err == nil && !valid {
		return domain.ErrLease
	}
	return err
}
