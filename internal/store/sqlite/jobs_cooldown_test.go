package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestCooldownDeferredDoesNotConsumeRetryAttempt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "cooldown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.Enqueue(ctx, "sync_following", "cooldown-following", json.RawMessage(`{"account_id":"test","cursor":""}`))
	if err != nil {
		t.Fatal(err)
	}
	base := time.UnixMilli(time.Now().Add(time.Second).UnixMilli())
	for attempt := 1; attempt <= 4; attempt++ {
		job, claimErr := s.Claim(ctx, base.Add(time.Duration(attempt)*time.Second))
		if claimErr != nil {
			t.Fatalf("Claim attempt %d: %v", attempt, claimErr)
		}
		if job.ID != id || job.Attempt != attempt {
			t.Fatalf("Claimed job = %+v, want ID %q attempt %d", job, id, attempt)
		}
		if finishErr := s.Finish(ctx, job, "retry_wait", "TEMPORARY", base.Add(time.Duration(attempt+1)*time.Second)); finishErr != nil {
			t.Fatalf("Finish attempt %d: %v", attempt, finishErr)
		}
	}

	fifth, err := s.Claim(ctx, base.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if fifth.Attempt != 5 {
		t.Fatalf("Claim attempt = %d, want 5", fifth.Attempt)
	}
	available := base.Add(2 * time.Minute)
	if err := s.Finish(ctx, fifth, "deferred", "RATE_LIMITED", available); err != nil {
		t.Fatalf("Finish deferred: %v", err)
	}

	got, err := s.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "retry_wait" || got.Attempt != 4 || got.AvailableAt != available.UnixMilli() || got.ErrorCode != "RATE_LIMITED" {
		t.Fatalf("deferred job = %+v; want retry_wait attempt 4 available at %d", got, available.UnixMilli())
	}
	if _, err := s.Claim(ctx, available.Add(-time.Millisecond)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Claim before cooldown expiry = %v, want ErrNotFound", err)
	}

	retry, err := s.Claim(ctx, available)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Attempt != 5 {
		t.Fatalf("Claim after cooldown attempt = %d, want 5", retry.Attempt)
	}
	if err := s.Finish(ctx, retry, "succeeded", "", available.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err = s.Job(ctx, id)
	if err != nil || got.State != "succeeded" || got.Attempt != 5 {
		t.Fatalf("successful job = %+v, err=%v; want succeeded at attempt 5", got, err)
	}
}

func TestCooldownDeferredStillHonorsAttemptLimitForRealRetry(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.Enqueue(ctx, "sync_following", "real-retry-at-limit", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	base := time.UnixMilli(time.Now().Add(time.Second).UnixMilli())
	for attempt := 1; attempt <= 5; attempt++ {
		job, err := s.Claim(ctx, base.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatalf("Claim attempt %d: %v", attempt, err)
		}
		if job.Attempt != attempt {
			t.Fatalf("Claim attempt = %d, want %d", job.Attempt, attempt)
		}
		if err := s.Finish(ctx, job, "retry_wait", "RATE_LIMITED", base.Add(time.Duration(attempt+1)*time.Second)); err != nil {
			t.Fatalf("Finish attempt %d: %v", attempt, err)
		}
	}
	jobs, err := s.Jobs(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Attempt != 5 || jobs[0].ErrorCode != "RATE_LIMITED" {
		t.Fatalf("job after real retry limit = %+v; want failed at attempt 5", jobs)
	}
}

func TestDeferredFinishFencesStaleLeaseAndCancellationWins(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	staleID, err := s.Enqueue(ctx, "sync_following", "deferred-stale", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	staleJob, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	staleJob.LeaseToken = "not-the-current-lease"
	if err := s.Finish(ctx, staleJob, "deferred", "RATE_LIMITED", time.Now().Add(time.Minute)); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("stale deferred Finish error = %v, want ErrLease", err)
	}
	got, err := s.Job(ctx, staleID)
	if err != nil || got.State != "running" || got.Attempt != 1 {
		t.Fatalf("stale lease changed job = %+v, err=%v", got, err)
	}

	cancelID, err := s.Enqueue(ctx, "sync_following", "deferred-cancel", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	cancelJob, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if cancelJob.ID != cancelID {
		t.Fatalf("claimed ID = %q, want %q", cancelJob.ID, cancelID)
	}
	if err := s.Cancel(ctx, cancelID); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, cancelJob, "deferred", "RATE_LIMITED", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("deferred Finish after cancel request: %v", err)
	}
	got, err = s.Job(ctx, cancelID)
	if err != nil || got.State != "canceled" || got.ErrorCode != "" {
		t.Fatalf("canceled deferred job = %+v, err=%v; want canceled with cleared error", got, err)
	}
}

func TestClaimPrioritizesFollowingAndKeepsHistoryLowest(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ordinaryID, err := s.Enqueue(ctx, "sync_artist", "priority-ordinary", json.RawMessage(`{"artist_id":"1","phase":"recent"}`))
	if err != nil {
		t.Fatal(err)
	}
	historyID, err := s.Enqueue(ctx, "sync_artist", "priority-history", json.RawMessage(`{"artist_id":"2","phase":"history"}`))
	if err != nil {
		t.Fatal(err)
	}
	followingID, err := s.Enqueue(ctx, "sync_following", "priority-following", json.RawMessage(`{"account_id":"test","cursor":""}`))
	if err != nil {
		t.Fatal(err)
	}

	now := time.UnixMilli(time.Now().Add(time.Second).UnixMilli())
	following, err := s.Claim(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if following.ID != followingID {
		t.Fatalf("first claimed ID = %q, want following %q", following.ID, followingID)
	}
	if err := s.Finish(ctx, following, "succeeded", "", now); err != nil {
		t.Fatal(err)
	}

	ordinary, err := s.Claim(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.ID != ordinaryID {
		t.Fatalf("second claimed ID = %q, want ordinary %q", ordinary.ID, ordinaryID)
	}
	if err := s.Finish(ctx, ordinary, "succeeded", "", now); err != nil {
		t.Fatal(err)
	}
	history, err := s.Claim(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if history.ID != historyID {
		t.Fatalf("third claimed ID = %q, want history %q", history.ID, historyID)
	}
}

func TestUnavailableFollowingDoesNotBlockOrdinaryClaim(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	followingID, err := s.Enqueue(ctx, "sync_following", "unavailable-following", json.RawMessage(`{"account_id":"test","cursor":""}`))
	if err != nil {
		t.Fatal(err)
	}
	ordinaryID, err := s.Enqueue(ctx, "sync_artist", "available-ordinary", json.RawMessage(`{"artist_id":"1","phase":"recent"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(time.Now().Add(time.Second).UnixMilli())
	future := now.Add(time.Hour)
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET available_at=? WHERE id=?`, future.UnixMilli(), followingID); err != nil {
		t.Fatal(err)
	}

	job, err := s.Claim(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != ordinaryID {
		t.Fatalf("claimed ID = %q, want available ordinary %q", job.ID, ordinaryID)
	}
}
