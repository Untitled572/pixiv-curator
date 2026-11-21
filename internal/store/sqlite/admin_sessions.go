package sqlite

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) SaveRememberedSession(ctx context.Context, session, csrf [32]byte, expires, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM remembered_sessions WHERE expires_at<=?`, now.UnixMilli()); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO remembered_sessions(session_hash,csrf_hash,expires_at)
		SELECT ?,?,? WHERE (SELECT COUNT(*) FROM remembered_sessions)<256`, session[:], csrf[:], expires.UnixMilli())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, tx.Commit()
}

func (s *Store) RememberedSession(ctx context.Context, session [32]byte) ([32]byte, time.Time, error) {
	var hash [32]byte
	var csrf []byte
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT csrf_hash,expires_at FROM remembered_sessions WHERE session_hash=?", session[:]).Scan(&csrf, &expires)
	if err == sql.ErrNoRows {
		return hash, time.Time{}, nil
	}
	if err != nil {
		return hash, time.Time{}, err
	}
	copy(hash[:], csrf)
	return hash, time.UnixMilli(expires), nil
}

func (s *Store) DeleteRememberedSession(ctx context.Context, session [32]byte) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM remembered_sessions WHERE session_hash=?", session[:])
	return err
}
