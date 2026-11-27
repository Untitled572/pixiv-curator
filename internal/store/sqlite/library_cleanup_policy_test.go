package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestCleanupPendingAIWaitsForEvidenceAndSurvivesUntilAdmission(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 1)
	now := time.Now().UnixMilli()
	enabled := now - int64(time.Hour/time.Millisecond)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1,cleanup_enabled_at=? WHERE id=1`, enabled); err != nil {
		t.Fatal(err)
	}
	add := func(id int64, ai string, created int64) {
		t.Helper()
		_, err := s.db.Exec(`INSERT INTO artworks(id,artist_id,kind,page_count,rating,ai_status,metadata_state,visible,created_at,updated_at,published_at) VALUES(?,1,'illust',1,'safe',?,'complete',1,?,?,1)`, id, ai, created, created)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(?,0,'https://i.pximg.net/a',100,100,'indexed',?,?)`, id, created, created)
		if err != nil {
			t.Fatal(err)
		}
	}
	add(101, "ai", enabled+1)
	round, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("one preview admitted: %+v", items)
	}
	var pending string
	if err = s.db.QueryRow(`SELECT pending_reason FROM library_cleanup_skip WHERE artist_id=1`).Scan(&pending); err != nil || pending != "new_ai" {
		t.Fatalf("pending=%q err=%v", pending, err)
	}
	add(102, "non_ai", enabled+2)
	add(103, "non_ai", enabled+3)
	round, items, _, err = s.NextLibraryCleanup(ctx, round)
	if err != nil || len(items) != 1 {
		t.Fatalf("admission: %+v %v", items, err)
	}
	if items[0].SlotType != "priority" || items[0].AuditReason != "new_ai" || len(items[0].Samples) == 0 || items[0].Samples[0].IllustID != "101" || items[0].Samples[0].Reason != "ai" {
		t.Fatalf("AI exception lost: %+v", items[0])
	}
	if err = s.db.QueryRow("SELECT pending_reason FROM library_cleanup_skip WHERE artist_id=1").Scan(&pending); err != nil || pending != "new_ai" {
		t.Fatalf("admission consumed the mark before a new cooldown was set: pending=%q err=%v", pending, err)
	}
	if err = s.KeepLibraryCleanupArtist(ctx, 1, round); err != nil {
		t.Fatal(err)
	}
	_, items, _, err = s.NextLibraryCleanup(ctx, round)
	if err != nil || len(items) != 0 {
		t.Fatalf("old AI retriggered: %+v %v", items, err)
	}
	if err = s.db.QueryRow(`SELECT pending_reason FROM library_cleanup_skip WHERE artist_id=1`).Scan(&pending); err != nil || pending != "" {
		t.Fatalf("pending after keep=%q err=%v", pending, err)
	}
}

func TestCleanupTwoNewDislikesThawAndOldEvidenceDoesNot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 1)
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{101, 102} {
		if _, err := s.db.Exec(`INSERT INTO artworks(id,artist_id,page_count,visible,created_at,updated_at) VALUES(?,1,1,1,?,?)`, id, now-10000, now-10000); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(?,0,'https://i.pximg.net/a',1,1,'indexed',?,?)`, id, now-10000, now-10000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO library_cleanup_skip(artist_id,until_round,started_at,min_at,max_at) VALUES(1,100,?,?,?)`, now-5000, now+100000, now+200000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO image_feedback VALUES(101,0,'dislike',?)`, now-6000); err != nil {
		t.Fatal(err)
	}
	round, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("old dislike thawed: %+v %v", items, err)
	}
	// Repeating an old vote does not turn it into a new event, and a retracted
	// new vote cannot combine with it to thaw the cooldown.
	if _, err = s.SetImageFeedback(ctx, 101, 0, "dislike"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetImageFeedback(ctx, 102, 0, "dislike"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetImageFeedback(ctx, 102, 0, "neutral"); err != nil {
		t.Fatal(err)
	}
	round, items, _, err = s.NextLibraryCleanup(ctx, round)
	if err != nil || len(items) != 0 {
		t.Fatalf("repeat/retracted vote thawed: %+v %v", items, err)
	}
	if _, err = s.db.Exec(`UPDATE image_feedback SET updated_at=? WHERE illust_id=101; INSERT INTO image_feedback VALUES(102,0,'dislike',?)`, now-1000, now-1000); err != nil {
		t.Fatal(err)
	}
	_, items, _, err = s.NextLibraryCleanup(ctx, round)
	if err != nil || len(items) != 1 || items[0].AuditReason != "new_dislikes" {
		t.Fatalf("new dislikes: %+v %v", items, err)
	}
}

func TestCleanupPendingExceptionWaitsForPrioritySlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UnixMilli()
	for id := int64(1); id <= 5; id++ {
		seedArtist(t, s, id)
		if _, err := s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO library_cleanup_skip(artist_id,until_round,started_at,min_at,max_at,pending_reason) VALUES(?,100,?,?,?,'new_dislikes')`, id, now-1000, now+100000, now+200000); err != nil {
			t.Fatal(err)
		}
	}
	round, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("expected four priority slots: %+v", items)
	}
	for _, item := range items {
		if item.SlotType != "priority" {
			t.Fatalf("pending exception entered routine slot: %+v", item)
		}
	}
	var pending int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM library_cleanup_skip s WHERE pending_reason='new_dislikes' AND NOT EXISTS (SELECT 1 FROM library_cleanup_batch b WHERE b.artist_id=s.artist_id)`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	_, items, _, err = s.NextLibraryCleanup(ctx, round)
	if err != nil || len(items) != 1 || items[0].SlotType != "priority" {
		t.Fatalf("waiting exception: %+v %v", items, err)
	}
}
func TestCleanupHistoricalSamplesUseDefaultEligibility(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 1)
	now := time.Now().UnixMilli()
	day := int64((24 * time.Hour) / time.Millisecond)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 7; i++ {
		id := 100 + i
		published := now - (40+i)*day
		rating := "safe"
		bookmarks := i
		if i <= 3 {
			published = now - i*day
		}
		if i == 4 {
			rating = "r18"
			bookmarks = 10000
		}
		if _, err := s.db.Exec(`INSERT INTO artworks(id,artist_id,kind,page_count,rating,ai_status,bookmarks,published_at,metadata_state,visible,created_at,updated_at) VALUES(?,1,'illust',1,?,'non_ai',?,?,'complete',1,?,?)`, id, rating, bookmarks, published, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(?,0,'https://i.pximg.net/a',100,100,'indexed',?,?)`, id, now, now); err != nil {
			t.Fatal(err)
		}
	}
	_, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if len(items[0].Samples) != 6 {
		t.Fatalf("samples=%+v", items[0].Samples)
	}
	for _, sample := range items[0].Samples {
		if sample.IllustID == "104" {
			t.Fatalf("ineligible high bookmark work outranked default eligible works: %+v", items[0].Samples)
		}
	}
}

func TestCleanupCountsR18BranchTowardAvailability(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 1)
	if _, err := s.db.ExecContext(ctx, "UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for id := int64(101); id <= 110; id++ {
		seedSelectionArtwork(t, s, id, 1, "r18", "non_ai", nil, 1)
	}
	_, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	item := items[0]
	if item.AuditReason == "low_availability" || item.Summary.EligiblePages != 0 {
		t.Fatalf("R18 supply should count toward review without changing default eligibility: %+v", item)
	}
}

func TestCleanupPrioritizesArtistsWithMostlyTwoOrThreeRecentPages(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for artist, count := range []int{120, 10, 9} {
		id := int64(artist + 1)
		seedEligibleArtist(t, s, id, "approved")
		for i := range count {
			pages := 1
			// Artist 1 has 51 short works in the latest 100, even though
			// they are less than half of its 120 works overall.
			if artist == 0 && i >= 69 || artist == 1 && i >= 5 || artist == 2 {
				pages = 2 + i%2
			}
			seedSelectionArtwork(t, s, id*1000+int64(i), id, "safe", "non_ai", nil, pages)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET published_at=id`); err != nil {
		t.Fatal(err)
	}
	_, items, _, err := s.LibraryCleanupReview(ctx)
	if err != nil || len(items) != 3 {
		t.Fatalf("review items=%+v err=%v", items, err)
	}
	if first := items[0]; first.Artist.ID != 1 || first.SlotType != "priority" || first.AuditReason != "mostly_two_three_pages" {
		t.Fatalf("expected short-work priority, got %+v", first)
	}
	for _, item := range items[1:] {
		if item.AuditReason == "mostly_two_three_pages" {
			t.Fatalf("artist below the sample or proportion threshold matched: %+v", item)
		}
	}
}
