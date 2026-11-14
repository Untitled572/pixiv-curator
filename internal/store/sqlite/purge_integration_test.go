package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestPurgeMigrationRetainsActiveAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 13; i++ {
		body, e := migrations.ReadFile("migrations/" + entries[i].Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(body)); e != nil {
			t.Fatalf("migration %d: %v", i+1, e)
		}
		if _, e = db.Exec(`INSERT INTO schema_migrations VALUES(?,1)`, i+1); e != nil {
			t.Fatal(e)
		}
	}
	for _, x := range []struct {
		id      int
		status  string
		enabled int
	}{{1, "approved", 0}, {2, "rejected", 0}, {3, "approved", 1}} {
		stmts := []struct {
			q string
			a []any
		}{
			{`INSERT INTO artists(id,review_status,collection_enabled,created_at,updated_at) VALUES(?,?,?,1,1)`, []any{x.id, x.status, x.enabled}},
			{`INSERT INTO artworks(id,artist_id,page_count,created_at,updated_at) VALUES(?,?,1,1,1)`, []any{x.id * 10, x.id}},
			{`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(?,0,'https://i.pximg.net/a',1,1,'indexed',1,1)`, []any{x.id * 10}},
			{`INSERT INTO selection_history VALUES(?, 'default',?,0,1,'req')`, []any{fmt.Sprint(x.id), x.id * 10}},
			{`INSERT INTO distribution_history VALUES(?, 'default',?,0,1,'req')`, []any{fmt.Sprint(x.id), x.id * 10}},
			{`INSERT INTO image_feedback VALUES(?,0,'like',1)`, []any{x.id * 10}},
			{`INSERT INTO artwork_preferences VALUES(?,'like',1)`, []any{x.id * 10}},
			{`INSERT INTO feedback_events VALUES(?,?,?,'like','test',1)`, []any{fmt.Sprint(x.id), fmt.Sprint(x.id), x.id * 10}},
			{`INSERT INTO artist_sync_state(artist_id,phase,cursor,run_id) VALUES(?,'history','old','run')`, []any{x.id}},
		}
		for _, st := range stmts {
			if _, err = db.Exec(st.q, st.a...); err != nil {
				t.Fatalf("%s: %v", st.q, err)
			}
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"artworks", "image_pages", "selection_history", "distribution_history", "image_feedback", "feedback_events", "artwork_preferences"} {
		var n int
		if err = s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
	var artist int
	if err = s.db.QueryRow(`SELECT artist_id FROM artworks`).Scan(&artist); err != nil || artist != 3 {
		t.Fatalf("artist=%d err=%v", artist, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after migration")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	stats, err := s.CatalogStats(ctx)
	if err != nil || stats.Artworks != 1 || stats.ImagePages != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestPauseSharedPreviewAndFollowingResume(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []int64{51, 52} {
		seedArtist(t, s, id)
		if _, err := s.DecideArtist(ctx, id, "approve"); err != nil {
			t.Fatal(err)
		}
		a := testArtwork(id*10, id)
		a.Pages[0].PreviewURL = "https://i.pximg.net/shared.jpg"
		if err := s.UpsertArtwork(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO artist_sync_state(artist_id,phase,cursor,run_id) VALUES(51,'history','old','run')`); err != nil {
		t.Fatal(err)
	}
	var urls []string
	s.SetPreviewPurgeHook(func(u []string) error { urls = u; return nil })
	if err := s.SetCollection(ctx, 51, false); err != nil {
		t.Fatal(err)
	}
	if len(urls) != 0 {
		t.Fatalf("shared URL purged: %v", urls)
	}
	if _, err := s.Enqueue(ctx, "sync_following", "following:pause-test", json.RawMessage(`{"run":"test"}`)); err != nil {
		t.Fatal(err)
	}
	job, err := claimJobByKey(t, s, "following:pause-test")
	if err != nil {
		t.Fatal(err)
	}
	fctx := domain.WithFollowVisibility(ctx, "public")
	if err = s.CommitFollowingBatch(fctx, job, "follow-run", []domain.Artist{{ID: 51, Name: "paused"}}, json.RawMessage(`{"cursor":"next"}`), false); err != nil {
		t.Fatal(err)
	}
	assertArtistState(t, s, 51, "approved", false)
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM jobs WHERE dedupe_key='artist:51' AND state IN ('pending','running','retry_wait','paused_auth')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("active jobs=%d err=%v", n, err)
	}
	if err = s.SetCollection(ctx, 51, true); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err = s.db.QueryRow(`SELECT payload_json FROM jobs WHERE dedupe_key='artist:51' AND state='pending'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Phase  string `json:"phase"`
		Cursor string `json:"cursor"`
	}
	if err = json.Unmarshal([]byte(payload), &p); err != nil || p.Phase != "recent" || p.Cursor != "" {
		t.Fatalf("resume payload=%s err=%v", payload, err)
	}
	var states int
	if err = s.db.QueryRow(`SELECT count(*) FROM artist_sync_state WHERE artist_id=51`).Scan(&states); err != nil || states != 0 {
		t.Fatalf("states=%d err=%v", states, err)
	}
}

func TestPauseReclaimsDatabasePages(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedArtist(t, s, 81)
	if _, err = s.DecideArtist(ctx, 81, "approve"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if _, err = tx.Exec(`INSERT INTO artworks(id,artist_id,caption,page_count,created_at,updated_at) VALUES(?,81,?,0,1,1)`, 10000+i, strings.Repeat("x", 8192)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var pagesBefore, pagesAfter, free int
	if err = s.db.QueryRow(`PRAGMA page_count`).Scan(&pagesBefore); err != nil {
		t.Fatal(err)
	}
	if err = s.SetCollection(ctx, 81, false); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`PRAGMA page_count`).Scan(&pagesAfter); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if pagesAfter >= pagesBefore || after.Size() >= before.Size() {
		t.Fatalf("pages %d -> %d, size %d -> %d, free=%d", pagesBefore, pagesAfter, before.Size(), after.Size(), free)
	}
	t.Logf("pages %d -> %d, file %d -> %d bytes, free=%d", pagesBefore, pagesAfter, before.Size(), after.Size(), free)
}

func TestRecommendationSeedQueriesExcludePausedApproved(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 201, "approved")
	seedEligibleArtist(t, s, 202, "approved")
	seedEligibleArtist(t, s, 203, "pending")
	if _, err := s.db.Exec(`UPDATE artists SET collection_enabled=0,remote_followed=1 WHERE id=201`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE artists SET remote_followed=1 WHERE id=203`); err != nil {
		t.Fatal(err)
	}
	seeds, err := s.RecommendationSeeds(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]bool{}
	for _, a := range seeds {
		found[a.ID] = true
	}
	if found[201] || !found[202] || !found[203] {
		t.Fatalf("seeds=%v", found)
	}
	run, _, err := s.StartRecommendation(ctx, json.RawMessage(`{"seed_artists":10}`))
	if err != nil {
		t.Fatal(err)
	}
	seeds, err = s.RecommendationSeedsForRun(ctx, run, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range seeds {
		if a.ID == 201 {
			t.Fatalf("paused seed in frozen run: %+v", seeds)
		}
	}
	if err = s.SetCollection(ctx, 202, false); err != nil {
		t.Fatal(err)
	}
	seeds, err = s.RecommendationSeedsForRun(ctx, run, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range seeds {
		if a.ID == 202 {
			t.Fatalf("seed paused after freeze remains: %+v", seeds)
		}
	}
}
