// Package sqlite owns the local catalog and explicit schema migrations.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db               *sql.DB
	hookMu           sync.RWMutex
	previewPurgeHook func([]string) error
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("empty database path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		// A drive letter must be part of the URI path, not its authority:
		// file:///C:/... is valid while file://C:/... is parsed with authority C:.
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err = db.PingContext(ctx); err == nil {
		err = s.Migrate(ctx)
	}
	if err == nil {
		err = s.enableIncrementalVacuum(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Migrate rejects unknown or non-contiguous versions before applying any change.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	version := 0
	for rows.Next() {
		var v int
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		version++
		if v != version || v > len(entries) {
			rows.Close()
			return fmt.Errorf("unsupported schema version %d", v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i := version; i < len(entries); i++ {
		body, err := migrations.ReadFile("migrations/" + entries[i].Name())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(?,?)`, i+1, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Backup creates a transactionally consistent standalone database, including WAL data.
// The destination must not exist. Keep it on a private local directory.
func (s *Store) Backup(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("empty backup path")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// VACUUM INTO accepts an empty destination but never an existing populated DB.
	_, err = s.db.ExecContext(ctx, `VACUUM INTO '`+strings.ReplaceAll(path, "'", "''")+`'`)
	if err != nil {
		os.Remove(path)
	}
	return err
}

// SetPreviewPurgeHook sets the post-commit thumbnail cleanup callback.
func (s *Store) SetPreviewPurgeHook(hook func([]string) error) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.previewPurgeHook = hook
}

// LivePreviewURLs returns catalog references for cache pruning at startup.
func (s *Store) LivePreviewURLs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT preview_url FROM image_pages WHERE preview_url<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if err = rows.Scan(&u); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, rows.Err()
}

func (s *Store) enableIncrementalVacuum(ctx context.Context) error {
	var mode int
	if err := s.db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return err
	}
	if mode == 2 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA auto_vacuum=INCREMENTAL`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

func (s *Store) reclaimFreePages(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
