package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestSelectionSnapshotViewsAndAtomicReplacement(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 8401, "approved")
	seedSelectionArtwork(t, s, 8402, 8401, "safe", "non_ai", nil, 1)
	if _, err := s.db.ExecContext(ctx, `UPDATE image_pages SET width=1600,height=900 WHERE illust_id=8402`); err != nil {
		t.Fatal(err)
	}
	s.rebuildSelectionSnapshot(ctx)
	old := s.selectionSnapshot.Load()
	if old == nil || len(old.views["any"][8401][8402]) != 1 || len(old.views["landscape"][8401][8402]) != 1 || len(old.views["portrait"][8401][8402]) != 0 {
		t.Fatal("wrong initial views")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE image_pages SET width=900,height=1600 WHERE illust_id=8402`); err != nil {
		t.Fatal(err)
	}
	s.rebuildSelectionSnapshot(ctx)
	current := s.selectionSnapshot.Load()
	if current == old || len(current.views["portrait"][8401][8402]) != 1 || len(current.views["landscape"][8401][8402]) != 0 {
		t.Fatal("new view not published")
	}
	if len(old.views["landscape"][8401][8402]) != 1 {
		t.Fatal("published snapshot was mutated")
	}
}
func TestSnapshotRefreshRequestsCoalesce(t *testing.T) {
	s := openTestStore(t)
	s.RequestSelectionSnapshotRefresh()
	s.RequestSelectionSnapshotRefresh()
	if len(s.selectionSnapshotRefresh) != 1 {
		t.Fatalf("queued requests=%d", len(s.selectionSnapshotRefresh))
	}
}

func TestSnapshotSelectionHonorsLiveFiltersAndRevision(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 8501, "approved")
	seedSelectionArtwork(t, s, 8502, 8501, "safe", "non_ai", []string{"foo"}, 1)
	s.rebuildSelectionSnapshot(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := domain.DefaultRules()

	where, args, err := selectionWhere(rules, 0, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	works, ok, err := s.snapshotEligibleWorks(ctx, tx, rules, where, args, 0, "", nil)
	if err != nil || !ok || len(works) != 1 {
		t.Fatalf("snapshot hit=%v works=%d err=%v", ok, len(works), err)
	}
	tx.Rollback()
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET review_status='blocked' WHERE id=8501`); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, ok, err = s.snapshotEligibleWorks(ctx, tx, rules, where, args, 0, "", nil)
	if err != nil || ok {
		t.Fatalf("stale snapshot accepted: hit=%v err=%v", ok, err)
	}
}

func TestSnapshotCommonCandidatesEqualSQL(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 8601, "approved")
	seedSelectionArtwork(t, s, 8602, 8601, "safe", "non_ai", nil, 2)
	seedSelectionArtwork(t, s, 8603, 8601, "r18", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 8604, 8601, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 8605, 8601, "safe", "non_ai", nil, 1)
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET kind='manga' WHERE id=8604`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO image_feedback(illust_id,page_index,value,updated_at) VALUES(8602,1,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	s.rebuildSelectionSnapshot(ctx)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rules := domain.DefaultRules()
	recent := []int64{8605}
	where, args, err := selectionWhere(rules, 0, "", recent)
	if err != nil {
		t.Fatal(err)
	}
	works, ok, err := s.snapshotEligibleWorks(ctx, tx, rules, where, args, 0, "", recent)
	if err != nil || !ok {
		t.Fatalf("snapshot ok=%v err=%v", ok, err)
	}
	got := map[snapshotPageKey]bool{}
	for _, w := range works {
		for _, p := range w.pages {
			got[snapshotPageKey{p.page.IllustID, p.page.PageIndex}] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,p.page_index FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[snapshotPageKey]bool{}
	for rows.Next() {
		var k snapshotPageKey
		if err = rows.Scan(&k.illust, &k.page); err != nil {
			t.Fatal(err)
		}
		want[k] = true
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=%v SQL=%v", got, want)
	}
}
