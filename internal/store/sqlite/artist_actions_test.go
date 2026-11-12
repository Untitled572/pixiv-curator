package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestApproveAtomicallyChangesStateAndEnqueuesIdempotently(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7101)

	jobID, err := s.DecideArtist(ctx, 7101, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if jobID == "" {
		t.Fatal("approval did not return the created sync job ID")
	}
	assertArtistState(t, s, 7101, "approved", true)

	var jobs int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE dedupe_key=? AND state IN ('pending','running','retry_wait','paused_auth')`, fmt.Sprintf("artist:%d", 7101)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("active sync jobs = %d, want 1", jobs)
	}
	if _, err := s.DecideArtist(ctx, 7101, "approve"); err != nil {
		t.Fatalf("repeated approval: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE dedupe_key=?`, fmt.Sprintf("artist:%d", 7101)).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("total sync jobs after repeated approval = %d, want 1", jobs)
	}
}

func TestApproveRollsBackWhenTaskEnqueueFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7102)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_sync_insert BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'injected enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 7102, "approve"); err == nil {
		t.Fatal("approval succeeded despite injected task enqueue failure")
	}
	assertArtistState(t, s, 7102, "pending", false)
	var jobs int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("job count after rollback = %d, want 0", jobs)
	}
}

func TestBlockedArtistMustBeRestoredBeforeApproval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7103)
	if _, err := s.DecideArtist(ctx, 7103, "block"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 7103, "approve"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("approve while blocked error = %v, want ErrConflict", err)
	}
	assertArtistState(t, s, 7103, "blocked", false)
	if _, err := s.DecideArtist(ctx, 7103, "restore"); err != nil {
		t.Fatal(err)
	}
	assertArtistState(t, s, 7103, "pending", false)
	if _, err := s.DecideArtist(ctx, 7103, "approve"); err != nil {
		t.Fatalf("approve after restore: %v", err)
	}
	assertArtistState(t, s, 7103, "approved", true)
}

func TestPauseCollectionDoesNotChangeApproval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7104)
	if _, err := s.DecideArtist(ctx, 7104, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollection(ctx, 7104, false); err != nil {
		t.Fatal(err)
	}
	assertArtistState(t, s, 7104, "approved", false)
}

func TestCommitArtistBatchRequiresLiveLeaseAndApprovedEnabledArtist(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7105)
	if _, err := s.DecideArtist(ctx, 7105, "approve"); err != nil {
		t.Fatal(err)
	}
	job, err := claimJobByKey(t, s, fmt.Sprintf("artist:%d", 7105))
	if err != nil {
		t.Fatal(err)
	}
	good := testArtwork(7106, 7105)
	if err := s.CommitArtistBatch(ctx, job, 7105, "run-1", "full", "next", []domain.Artwork{good}); err != nil {
		t.Fatalf("commit valid live batch: %v", err)
	}
	if err := s.CommitArtistBatch(ctx, job, 7105, "run-1", "full", "next", []domain.Artwork{good}); err != nil {
		t.Fatalf("repeat committed batch: %v", err)
	}
	progress, err := s.Job(ctx, job.ID)
	if err != nil || progress.ProgressPages != 1 || progress.ProgressItems != 1 || progress.CreatedAt == 0 || progress.UpdatedAt == 0 {
		t.Fatalf("artist progress = %+v, err=%v; want one page/item and timestamps", progress, err)
	}
	if err := s.Finish(ctx, job, "succeeded", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitArtistBatch(ctx, job, 7105, "run-2", "full", "", []domain.Artwork{testArtwork(7107, 7105)}); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("commit with finished lease error = %v, want ErrLease", err)
	}

	seedArtist(t, s, 7110)
	if _, err := s.DecideArtist(ctx, 7110, "approve"); err != nil {
		t.Fatal(err)
	}
	pausedJob, err := claimJobByKey(t, s, fmt.Sprintf("artist:%d", 7110))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET collection_enabled=0 WHERE id=7110`); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitArtistBatch(ctx, pausedJob, 7110, "run-3", "full", "next", []domain.Artwork{testArtwork(7111, 7110)}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("commit for paused artist error = %v, want ErrConflict", err)
	}
	if artworkExists(t, s, 7111) {
		t.Fatal("batch for paused artist stored artwork")
	}
}

func TestCommitArtistBatchRollsBackWhenMiddleArtworkFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7120)
	if _, err := s.DecideArtist(ctx, 7120, "approve"); err != nil {
		t.Fatal(err)
	}
	job, err := claimJobByKey(t, s, fmt.Sprintf("artist:%d", 7120))
	if err != nil {
		t.Fatal(err)
	}
	valid := testArtwork(7121, 7120)
	invalid := testArtwork(7122, 7120)
	invalid.Pages[0].Width = -1
	err = s.CommitArtistBatch(ctx, job, 7120, "run-fail", "full", "next", []domain.Artwork{valid, invalid})
	if err == nil {
		t.Fatal("batch containing invalid middle item unexpectedly succeeded")
	}
	if artworkExists(t, s, 7121) || artworkExists(t, s, 7122) {
		t.Fatal("artwork from failed transaction remained in database")
	}
	var stateCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_sync_state WHERE artist_id=7120`).Scan(&stateCount); err != nil {
		t.Fatal(err)
	}
	if stateCount != 0 {
		t.Fatalf("sync state rows after rollback = %d, want 0", stateCount)
	}
}

func TestFollowingBatchesOnlyMarkUnfollowedOnCompleteSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedArtist(t, s, 7201)
	seedArtist(t, s, 7202)
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved',collection_enabled=1,remote_followed=1,last_seen_follow_run='older' WHERE id=7201`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved',collection_enabled=1 WHERE id=7202`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Enqueue(ctx, "sync_following", "following:test", json.RawMessage(`{"run":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := claimJobByKey(t, s, "following:test")
	if err != nil {
		t.Fatal(err)
	}
	newArtist := domain.Artist{ID: 7202, Name: "refreshed", ReviewStatus: "rejected"}
	if err := s.CommitFollowingBatch(ctx, job, "run-1", []domain.Artist{newArtist}, json.RawMessage(`{"cursor":"next"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitFollowingBatch(ctx, job, "run-1", []domain.Artist{newArtist}, json.RawMessage(`{"cursor":"next"}`), false); err != nil {
		t.Fatalf("repeat committed following page: %v", err)
	}
	assertFollowing(t, s, 7201, true, "approved", true)
	assertFollowing(t, s, 7202, true, "approved", true)

	if err := s.CommitFollowingBatch(ctx, job, "run-1", []domain.Artist{newArtist}, json.RawMessage(`{"cursor":""}`), true); err != nil {
		t.Fatal(err)
	}
	progress, err := s.Job(ctx, job.ID)
	if err != nil || progress.ProgressPages != 2 || progress.ProgressItems != 2 {
		t.Fatalf("following progress = %+v, err=%v; want two pages/items", progress, err)
	}
	assertFollowing(t, s, 7201, false, "approved", true)
	assertFollowing(t, s, 7202, true, "approved", true)
}

func TestFollowingBatchRollsBackEarlierItemsOnMidBatchError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	_, err := s.Enqueue(ctx, "sync_following", "following:rollback", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := claimJobByKey(t, s, "following:rollback")
	if err != nil {
		t.Fatal(err)
	}
	err = s.CommitFollowingBatch(ctx, job, "run-bad", []domain.Artist{
		{ID: 7301, Name: "would be inserted"},
		{ID: 0, Name: "invalid"},
	}, json.RawMessage(`{"cursor":"next"}`), false)
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CommitFollowingBatch error = %v, want ErrInvalid", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists WHERE id=7301`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("artists from rolled-back batch = %d, want 0", count)
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM jobs WHERE id=?`, job.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != string(job.Payload) {
		t.Fatalf("job payload changed to %s after rollback", payload)
	}
}

func claimJobByKey(t *testing.T, s *Store, key string) (domain.Job, error) {
	t.Helper()
	j, err := s.Claim(context.Background(), time.Now().Add(time.Second))
	if err != nil {
		return domain.Job{}, err
	}
	if j.Key != key {
		return domain.Job{}, fmt.Errorf("claimed key %q, want %q", j.Key, key)
	}
	return j, nil
}

func assertArtistState(t *testing.T, s *Store, id int64, status string, enabled bool) {
	t.Helper()
	var gotStatus string
	var gotEnabled bool
	if err := s.db.QueryRow(`SELECT review_status,collection_enabled FROM artists WHERE id=?`, id).Scan(&gotStatus, &gotEnabled); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotEnabled != enabled {
		t.Fatalf("artist %d state = %q/%v, want %q/%v", id, gotStatus, gotEnabled, status, enabled)
	}
}

func assertFollowing(t *testing.T, s *Store, id int64, followed bool, review string, enabled bool) {
	t.Helper()
	var gotFollowed bool
	var gotReview string
	var gotEnabled bool
	if err := s.db.QueryRow(`SELECT remote_followed,review_status,collection_enabled FROM artists WHERE id=?`, id).Scan(&gotFollowed, &gotReview, &gotEnabled); err != nil {
		t.Fatal(err)
	}
	if gotFollowed != followed || gotReview != review || gotEnabled != enabled {
		t.Fatalf("artist %d following/review/collection = %v/%q/%v, want %v/%q/%v", id, gotFollowed, gotReview, gotEnabled, followed, review, enabled)
	}
}

func artworkExists(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM artworks WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count != 0
}

func testArtwork(id, artistID int64) domain.Artwork {
	return domain.Artwork{ID: id, ArtistID: artistID, PageCount: 1, MetadataState: "complete", Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/test.jpg", Width: 100, Height: 100}}}
}

func TestAtomicFollowingCompletionClearsPreviousRetryError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if _, err := store.Enqueue(ctx, "sync_following", "following:retry-success", json.RawMessage("{}")); err != nil {
		t.Fatal(err)
	}
	first, err := claimJobByKey(t, store, "following:retry-success")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, first, "retry_wait", "NETWORK_ERROR", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, err := claimJobByKey(t, store, "following:retry-success")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitFollowingBatch(ctx, retry, retry.ID, nil, json.RawMessage("{}"), true); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Job(ctx, retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "succeeded" || completed.ErrorCode != "" || completed.Attempt != 2 {
		t.Fatalf("successful retry retained obsolete failure: %+v", completed)
	}
}
