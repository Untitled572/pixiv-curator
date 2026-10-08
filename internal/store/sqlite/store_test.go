package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenInitializesWALAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestOpenCanRepeatMigrationsAndReopenData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration: %v", err)
	}
	insertArtist(t, s, 42, "persistent")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM artists WHERE id=42").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "persistent" {
		t.Fatalf("name = %q, want persistent", name)
	}
	var migrations int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	wantMigrations := migrationCount(t)
	if migrations != wantMigrations {
		t.Fatalf("migration rows = %d, want %d", migrations, wantMigrations)
	}
}

func TestMigrateRejectsFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	versions := make([]int, migrationCount(t)+1)
	for i := range versions {
		versions[i] = i + 1
	}
	seedMigrationVersions(t, path, versions...)
	if s, err := Open(context.Background(), path); err == nil {
		s.Close()
		t.Fatal("Open accepted a schema version newer than available migrations")
	}
}

func TestMigrateRejectsVersionGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap.db")
	last := migrationCount(t) + 1
	seedMigrationVersions(t, path, 1, last)
	if s, err := Open(context.Background(), path); err == nil {
		s.Close()
		t.Fatal("Open accepted a migration history with a gap")
	}
}

func migrationCount(t *testing.T) int {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestOpenAppliesMigrationToExistingEmptyHistoryAndPreservesOtherTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openRawDB(t, path)
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
		CREATE TABLE legacy_notes(note TEXT NOT NULL);
		INSERT INTO legacy_notes(note) VALUES ('keep me');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var note string
	if err := s.db.QueryRow("SELECT note FROM legacy_notes").Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "keep me" {
		t.Fatalf("legacy table value = %q, want preserved value", note)
	}
	var artistsTable int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='artists'").Scan(&artistsTable); err != nil {
		t.Fatal(err)
	}
	if artistsTable != 1 {
		t.Fatal("migration 001 was not applied to the existing database")
	}
}

func TestBackupIncludesCommittedWALDataAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "catalog.db")
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	insertArtist(t, s, 84, "in wal")

	walInfo, err := os.Stat(dbPath + "-wal")
	if err != nil {
		t.Fatalf("stat WAL file: %v", err)
	}
	if walInfo.Size() <= 32 {
		t.Fatalf("WAL file size = %d, want committed frames", walInfo.Size())
	}

	backupPath := filepath.Join(dir, "backup.db")
	if err := s.Backup(ctx, backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}
	backup := openRawDB(t, backupPath)
	var name string
	if err := backup.QueryRow("SELECT name FROM artists WHERE id=84").Scan(&name); err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if name != "in wal" {
		t.Fatalf("backup name = %q, want in wal", name)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(backupPath, []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, backupPath); err == nil {
		t.Fatal("Backup overwrote an existing destination")
	}
	contents, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "do not replace" {
		t.Fatalf("existing backup changed to %q", contents)
	}
}

func insertArtist(t *testing.T, s *Store, id int64, name string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO artists(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)`, id, name, now, now); err != nil {
		t.Fatal(err)
	}
}

func seedMigrationVersions(t *testing.T, path string, versions ...int) {
	t.Helper()
	db := openRawDB(t, path)
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range versions {
		if _, err := db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES(?, 0)", version); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
