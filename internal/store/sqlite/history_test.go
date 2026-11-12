package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestHistoryPaginationOrderingAvailabilityAndFixedURLs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1, "approved")
	seedEligibleArtist(t, s, 2, "blocked")
	seedEligibleArtist(t, s, 3, "approved")
	seedSelectionArtwork(t, s, 101, 1, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 102, 1, "r18", "ai", nil, 1)
	seedSelectionArtwork(t, s, 201, 2, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 301, 3, "safe", "non_ai", nil, 1)
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET visible=0 WHERE id=301`); err != nil {
		t.Fatal(err)
	}
	for i, record := range []struct {
		id       string
		illust   int64
		selected int64
	}{
		{id: "record-a", illust: 101, selected: 100},
		{id: "record-b", illust: 101, selected: 200},
		{id: "record-c", illust: 102, selected: 200},
		{id: "record-d", illust: 201, selected: 300},
		{id: "record-e", illust: 301, selected: 300},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,'preferred',?,0,?,?)`, record.id, record.illust, record.selected, fmt.Sprintf("request-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.History(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 5 || first.Page != 1 || first.PageSize != 2 || first.TotalPages != 3 || len(first.Items) != 2 {
		t.Fatalf("first history page=%+v", first)
	}
	if first.Items[0].ID != "record-e" || first.Items[1].ID != "record-d" || first.Items[0].Available || first.Items[1].Available {
		t.Fatalf("stable tie ordering or blocked/hidden availability failed: %+v", first.Items)
	}
	second, err := s.History(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].ID != "record-c" || !second.Items[0].Available || second.Items[1].ID != "record-b" || !second.Items[1].Available {
		t.Fatalf("management eligibility or duplicate page identity failed: %+v", second.Items)
	}
	if second.Items[0].ThumbnailURL != "/admin/api/previews/102/0" || second.Items[0].FullURL != "/admin/api/previews/102/0?size=original" || second.Items[1].ThumbnailURL != "/admin/api/previews/101/0" || second.Items[1].FullURL != "/admin/api/previews/101/0?size=original" || second.Items[1].IllustID != 101 || second.Items[1].PageIndex != 0 {
		t.Fatalf("history preview URL or fixed page identity=%+v", second.Items)
	}
	if third, err := s.History(ctx, 3, 2); err != nil || len(third.Items) != 1 || third.Items[0].ID != "record-a" {
		t.Fatalf("last page=%+v err=%v", third, err)
	}
	if beyond, err := s.History(ctx, 99, 2); err != nil || len(beyond.Items) != 0 || beyond.TotalPages != 3 {
		t.Fatalf("beyond last page=%+v err=%v", beyond, err)
	}
}

func TestHistoryEmptyAndRejectsInvalidPagination(t *testing.T) {
	s := openTestStore(t)
	empty, err := s.History(context.Background(), 1, 24)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Total != 0 || empty.TotalPages != 0 {
		t.Fatalf("empty history=%+v err=%v", empty, err)
	}
	for _, tc := range []struct{ page, size int }{{0, 1}, {1, 0}, {1, 97}} {
		if _, err := s.History(context.Background(), tc.page, tc.size); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("History(%d,%d) error=%v, want ErrInvalid", tc.page, tc.size, err)
		}
	}
}

func TestSelectWritesDistributionEventsAndCleanupKeepsThemAfterAntiRepeatExpiry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 900, "approved")
	seedSelectionArtwork(t, s, 9001, 900, "safe", "non_ai", nil, 1)
	request := domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"}, Orientation: "any"}}
	for i := 0; i < 25; i++ {
		if _, err := s.Select(ctx, request); err != nil {
			t.Fatalf("selection %d: %v", i, err)
		}
	}
	var antiRepeat, distribution int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM selection_history`).Scan(&antiRepeat); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM distribution_history`).Scan(&distribution); err != nil {
		t.Fatal(err)
	}
	if antiRepeat != selectionRecentMax || distribution != 25 {
		t.Fatalf("before cleanup: anti-repeat=%d distribution=%d", antiRepeat, distribution)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE selection_history SET selected_at=?`, time.Now().Add(-31*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupSelectionHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM selection_history`).Scan(&antiRepeat); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM distribution_history`).Scan(&distribution); err != nil {
		t.Fatal(err)
	}
	if antiRepeat != 0 || distribution != 25 {
		t.Fatalf("cleanup crossed retention boundaries: anti-repeat=%d distribution=%d", antiRepeat, distribution)
	}
}

func TestCleanupDistributionHistoryEnforcesThirtyDaysAndTenThousandRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 910, "approved")
	seedSelectionArtwork(t, s, 9101, 910, "safe", "non_ai", nil, 1)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES('expired','preferred',9101,0,?, 'expired')`, now.Add(-30*24*time.Hour-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,'preferred',9101,0,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < distributionHistoryKeep+1; i++ {
		id := fmt.Sprintf("bulk-%05d", i)
		selected := now.Add(-24 * time.Hour).Add(time.Duration(i) * time.Millisecond).UnixMilli()
		if _, err = stmt.ExecContext(ctx, id, selected, id); err != nil {
			stmt.Close()
			t.Fatal(err)
		}
	}
	if err = stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupSelectionHistory(ctx); err != nil {
		t.Fatal(err)
	}
	var count, expired int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM distribution_history`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM distribution_history WHERE id='expired'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if count != distributionHistoryKeep || expired != 0 {
		t.Fatalf("retained rows=%d expired=%d; want cap=%d and no expired rows", count, expired, distributionHistoryKeep)
	}
}

func TestDistributionHistoryMigrationBackfillsOnlyExistingSelectionRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	version, _ := findMigrationContaining(t, "CREATE TABLE distribution_history")
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < version-1; i++ {
		body, readErr := migrations.ReadFile("migrations/" + entries[i].Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply pre-history migration %s: %v", entries[i].Name(), err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, i+1, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO artists(id,name,created_at,updated_at) VALUES(920,'legacy artist',1,1)`,
		`INSERT INTO artworks(id,artist_id,title,kind,page_count,metadata_state,created_at,updated_at) VALUES(9201,920,'legacy title','illust',1,'complete',1,1)`,
		`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(9201,0,'https://i.pximg.net/img-original/9201_p0.jpg',1200,900,'indexed',1,1)`,
		`INSERT INTO selection_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES('existing-selection','preferred',9201,0,12345,'request-1')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var count int
	var id, requestID string
	var selectedAt int64
	if err := upgraded.db.QueryRowContext(ctx, `SELECT count(*),min(id),min(request_id),min(selected_at) FROM distribution_history`).Scan(&count, &id, &requestID, &selectedAt); err != nil {
		t.Fatal(err)
	}
	if count != 1 || id != "existing-selection" || requestID != "request-1" || selectedAt != 12345 {
		t.Fatalf("migration backfill count=%d id=%q request=%q time=%d", count, id, requestID, selectedAt)
	}
}

func findMigrationContaining(t *testing.T, fragment string) (int, string) {
	t.Helper()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range entries {
		body, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(body)), strings.ToLower(fragment)) {
			return i + 1, string(body)
		}
	}
	t.Fatalf("no migration contains %q", fragment)
	return 0, ""
}
