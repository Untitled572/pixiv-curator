package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func (s *Store) Accounts(ctx context.Context) ([]domain.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.name,a.pixiv_id,a.token_file,
 COALESCE((SELECT state FROM jobs WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')=a.id ORDER BY created_at DESC,rowid DESC LIMIT 1),'pending'),
 COALESCE((SELECT MAX(updated_at) FROM jobs WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')=a.id AND state='succeeded'),0),
 COALESCE((SELECT error_code FROM jobs WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')=a.id ORDER BY created_at DESC,rowid DESC LIMIT 1),''),
 a.is_default,a.follow_on_approve,
 (SELECT COUNT(*) FROM account_artists aa WHERE aa.account_id=a.id AND aa.followed=1),
 (SELECT COUNT(*) FROM jobs j WHERE j.kind='follow_artist' AND json_extract(j.payload_json,'$.account_id')=a.id AND j.state IN ('pending','running','retry_wait','paused_auth') AND j.cancel_requested=0),
 (SELECT COUNT(*) FROM account_artists aa WHERE aa.account_id=a.id AND aa.followed=1 AND aa.visibility='private')
 FROM accounts a WHERE a.deleted=0 ORDER BY a.is_default DESC,a.created_at,a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Account{}
	for rows.Next() {
		var a domain.Account
		if err = rows.Scan(&a.ID, &a.Name, &a.PixivID, &a.TokenFile, &a.SyncState, &a.LastSyncedAt, &a.ErrorCode, &a.IsDefault, &a.FollowOnApprove, &a.FollowedArtists, &a.PendingFollows, &a.PrivateFollowedArtists); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (s *Store) SaveAccount(ctx context.Context, a domain.Account) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var duplicate int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE pixiv_id=? AND pixiv_id>0 AND deleted=0 AND id<>?`, a.PixivID, a.ID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate > 0 {
		return domain.ErrConflict
	}
	now := time.Now().UnixMilli()
	var activeAccounts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE deleted=0`).Scan(&activeAccounts); err != nil {
		return err
	}
	isDefault := activeAccounts == 0
	result, err := tx.ExecContext(ctx, `INSERT INTO accounts(id,name,pixiv_id,token_file,is_default,created_at,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,pixiv_id=excluded.pixiv_id,token_file=excluded.token_file,updated_at=excluded.updated_at WHERE accounts.deleted=0`, a.ID, a.Name, a.PixivID, a.TokenFile, isDefault, now, now)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected != 1 {
		return domain.ErrNotFound
	}
	// Credential replacement resumes only this account's interrupted work.
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='pending',attempt=0,error_code='',available_at=?,updated_at=? WHERE state='paused_auth' AND json_extract(payload_json,'$.account_id')=? AND cancel_requested=0`, now, now, a.ID)
	if err != nil {
		return err
	}
	if _, err = enqueueFollowing(ctx, tx, a.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func enqueueFollowing(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"account_id": id, "cursor": ""})
	return enqueue(ctx, tx, "sync_following", "following:"+id, payload)
}
func (s *Store) SyncAccount(ctx context.Context, id string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var active int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=? AND deleted=0`, id).Scan(&active)
	if err != nil {
		return "", err
	}
	if active == 0 {
		return "", domain.ErrNotFound
	}
	job, err := enqueueFollowing(ctx, tx, id)
	if err != nil {
		return "", err
	}
	return job, tx.Commit()
}
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var wasDefault bool
	if err = tx.QueryRowContext(ctx, `SELECT is_default FROM accounts WHERE id=? AND deleted=0`, id).Scan(&wasDefault); err == sql.ErrNoRows {
		return domain.ErrNotFound
	} else if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	r, err := tx.ExecContext(ctx, `UPDATE accounts SET deleted=1,is_default=0,updated_at=? WHERE id=? AND deleted=0`, now, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return domain.ErrNotFound
	}
	if wasDefault {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET is_default=1,updated_at=? WHERE id=(SELECT id FROM accounts WHERE deleted=0 ORDER BY created_at,id LIMIT 1)`, now)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE json_extract(payload_json,'$.account_id')=? AND state IN ('pending','running','retry_wait','paused_auth')`, now, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE account_artists SET followed=0 WHERE account_id=?`, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE artists SET remote_followed=EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=artists.id AND aa.followed=1 AND ac.deleted=0)`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ConfigureAccount atomically updates the active default and follow-on-approve option.
// A false makeDefault leaves the existing default unchanged.
func (s *Store) ConfigureAccount(ctx context.Context, id string, makeDefault bool, followOnApprove *bool) error {
	if id == "" || strings.TrimSpace(id) != id {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id=? AND deleted=0`, id).Scan(&active); err != nil {
		return err
	}
	if active == 0 {
		return domain.ErrNotFound
	}
	now := time.Now().UnixMilli()
	if makeDefault {
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET is_default=0,updated_at=? WHERE deleted=0 AND is_default=1`, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET is_default=1,updated_at=? WHERE id=? AND deleted=0`, now, id); err != nil {
			return err
		}
	}
	if followOnApprove != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET follow_on_approve=?,updated_at=? WHERE id=? AND deleted=0`, *followOnApprove, now, id); err != nil {
			return err
		}
		if !*followOnApprove {
			_, err = tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1,state=CASE WHEN state='running' THEN state ELSE 'canceled' END,updated_at=? WHERE kind='follow_artist' AND json_extract(payload_json,'$.account_id')=? AND json_extract(payload_json,'$.origin')='approval' AND state IN ('pending','running','retry_wait','paused_auth')`, now, id)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) LegacyImported(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE id='legacy'`).Scan(&n)
	return n > 0, err
}
func (s *Store) AdoptLegacyFollowing(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) SELECT 'legacy',id,1 FROM artists WHERE remote_followed=1 ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET payload_json=json_set(payload_json,'$.account_id','legacy') WHERE json_extract(payload_json,'$.account_id') IS NULL AND kind='sync_following'`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BindAccountIdentity(ctx context.Context, id string, uid int64) error {
	if uid <= 0 {
		return domain.ErrInvalid
	}
	var duplicate bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id<>? AND pixiv_id=? AND deleted=0)`, id, uid).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return domain.ErrConflict
	}
	r, err := s.db.ExecContext(ctx, `UPDATE accounts SET pixiv_id=?,updated_at=? WHERE id=? AND deleted=0 AND (pixiv_id=0 OR pixiv_id=?)`, uid, time.Now().UnixMilli(), id, uid)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return domain.ErrConflict
	}
	return nil
}
func (s *Store) DeletedAccountFiles(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_file FROM accounts WHERE deleted=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return result, rows.Err()
}
