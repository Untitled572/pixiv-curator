package sqlite

import (
	"context"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"testing"
)

func TestAccountFollowingUnionAndExplicitExclusions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []string{"first", "second"} {
		if err := s.SaveAccount(ctx, domain.Account{ID: id, Name: id, TokenFile: "unused"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{11, 12, 13} {
		seedArtist(t, s, id)
	}
	if _, err := s.DecideArtist(ctx, 12, "block"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 13, "reject"); err != nil {
		t.Fatal(err)
	}
	sync := func(account string, ids ...int64) {
		t.Helper()
		if _, err := s.SyncAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET available_at=CASE WHEN dedupe_key=? THEN 0 ELSE 9999999999999 END WHERE state='pending'`, "following:"+account); err != nil {
			t.Fatal(err)
		}
		j, err := claimJobByKey(t, s, "following:"+account)
		if err != nil {
			t.Fatal(err)
		}
		a := []domain.Artist{}
		for _, id := range ids {
			a = append(a, domain.Artist{ID: id, Name: "artist"})
		}
		p, _ := json.Marshal(map[string]string{"account_id": account, "cursor": ""})
		batchCtx := domain.WithFollowVisibility(domain.WithAccount(ctx, account), "private")
		if err = s.CommitFollowingBatch(batchCtx, j, j.ID, a, p, true); err != nil {
			t.Fatal(err)
		}
	}
	sync("first", 11, 12, 13)
	sync("second", 11)
	assertArtistState(t, s, 11, "approved", true)
	assertArtistState(t, s, 12, "blocked", false)
	assertArtistState(t, s, 13, "rejected", false)
	sync("first")
	assertFollowing(t, s, 11, true, "approved", true)
	if err := s.DeleteAccount(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	assertFollowing(t, s, 11, false, "approved", true)
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_artists WHERE artist_id=11`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("history lost: %d %v", count, err)
	}
}
func TestRecentScanAtomicallyQueuesHistoryAndFencesDuplicates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 51)
	if _, err := s.DecideArtist(ctx, 51, "approve"); err != nil {
		t.Fatal(err)
	}
	j, err := claimJobByKey(t, s, "artist:51")
	if err != nil {
		t.Fatal(err)
	}
	works := make([]domain.Artwork, 100)
	for i := range works {
		works[i] = domain.Artwork{ID: int64(1000 + i), ArtistID: 51, Kind: "illust", Rating: "safe", AIStatus: "non_ai", MetadataState: "complete", PageCount: 0}
	}
	if err = s.CommitArtistBatch(ctx, j, 51, j.ID, "recent", "next-page", works); err != nil {
		t.Fatal(err)
	}
	finished, err := s.Job(ctx, j.ID)
	if err != nil || finished.State != "succeeded" || finished.ProgressItems != 100 {
		t.Fatalf("recent: %+v %v", finished, err)
	}
	history, err := claimJobByKey(t, s, "artist-history:51")
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Cursor, Phase string }
	if json.Unmarshal(history.Payload, &p) != nil || p.Cursor != "next-page" || p.Phase != "history" {
		t.Fatalf("lost handoff: %s", history.Payload)
	}
	if err = s.CommitArtistBatch(ctx, j, 51, j.ID, "recent", "next-page", works); err != domain.ErrLease {
		t.Fatalf("stale commit: %v", err)
	}
}
