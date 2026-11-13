package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAITagMigrationBackfillsPositiveEvidenceAndPreservesMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, _ := migrations.ReadDir("migrations")
	for i, e := range entries[:11] {
		raw, _ := migrations.ReadFile("migrations/" + e.Name())
		if _, err = d.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err = d.Exec(`INSERT INTO schema_migrations VALUES(?,0)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.Exec(`INSERT INTO artists(id,created_at,updated_at) VALUES(1,1,1);INSERT INTO artworks(id,artist_id,title,page_count,ai_status,created_at,updated_at) VALUES(150593377,1,'keep',0,'non_ai',1,1);INSERT INTO tags(id,normalized_name,display_name) VALUES(1,'aiイラスト','AIイラスト');INSERT INTO artwork_tags VALUES(150593377,1)`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var status, title string
	if err = s.db.QueryRow(`SELECT ai_status,title FROM artworks WHERE id=150593377`).Scan(&status, &title); err != nil || status != "ai" || title != "keep" {
		t.Fatalf("status=%s title=%s err=%v", status, title, err)
	}
}
