package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestConcurrentEnqueueDeduplicatesAndClaimHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "jobs.db")
	const workers = 12
	stores := make([]*Store, workers)
	for i := range stores {
		var err error
		stores[i], err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[i].Close()
	}

	ids := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = stores[i].Enqueue(ctx, "sync_artist", "same-key", json.RawMessage(`{"artist_id":"9"}`))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("Enqueue returned distinct active IDs: %v", ids)
		}
	}

	winners := 0
	var winner domain.Job
	results := make([]claimResult, workers)
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j, err := stores[i].Claim(ctx, time.Now())
			if err == nil {
				results[i] = claimResult{job: j}
				return
			}
			results[i] = claimResult{err: err}
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		if result.err == nil {
			winners++
			winner = result.job
		} else if !errors.Is(result.err, domain.ErrNotFound) {
			t.Fatalf("Claim %d: %v", i, result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful claim count = %d, want 1", winners)
	}
	if winner.ID != ids[0] || winner.State != "running" || winner.Attempt != 1 || winner.LeaseToken == "" {
		t.Fatalf("claimed job = %+v", winner)
	}
}

type claimResult struct {
	job domain.Job
	err error
}

func TestExpiredLeaseRejectsRenewAndFinish(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.Enqueue(ctx, "sync_artist", "expired", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now()
	j, err := s.Claim(ctx, claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), j.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Renew(ctx, j, time.Now()); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("Renew expired lease error = %v, want ErrLease", err)
	}
	if err := s.Finish(ctx, j, "succeeded", "", time.Now()); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("Finish expired lease error = %v, want ErrLease", err)
	}
}

func TestExpiredFifthAttemptPersistsFailureWhenNothingCanBeClaimed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exhausted.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, "sync_artist", "exhausted", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(time.Second)
	var j domain.Job
	for attempt := 1; attempt <= 5; attempt++ {
		j, err = s.Claim(ctx, base.Add(time.Duration(attempt)*100*time.Millisecond))
		if err != nil {
			t.Fatalf("Claim attempt %d: %v", attempt, err)
		}
		if j.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", j.Attempt, attempt)
		}
		if attempt < 5 {
			if err := s.Finish(ctx, j, "retry_wait", "TEMPORARY", base.Add(time.Duration(attempt+1)*100*time.Millisecond)); err != nil {
				t.Fatalf("Finish attempt %d: %v", attempt, err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Claim(ctx, base.Add(5*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Claim after expired fifth attempt = %v, want ErrNotFound", err)
	}
	jobs, err := s.Jobs(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Attempt != 5 {
		t.Fatalf("persisted job after exhausted lease = %+v, want failed at attempt 5", jobs)
	}
}

func TestCancellationAndLeaseRecoverySurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	pendingID, err := s.Enqueue(ctx, "sync_artist", "pending-cancel", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, pendingID); err != nil {
		t.Fatal(err)
	}
	runningID, err := s.Enqueue(ctx, "sync_artist", "running-cancel", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now()
	j, err := s.Claim(ctx, claimedAt)
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != runningID {
		t.Fatalf("claimed ID = %q, want %q", j.ID, runningID)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), j.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, runningID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Claim(ctx, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Claim after restart/cancellation error = %v, want ErrNotFound", err)
	}
	jobs, err := s.Jobs(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, job := range jobs {
		states[job.ID] = job.State
	}
	if states[pendingID] != "canceled" || states[runningID] != "canceled" {
		t.Fatalf("persisted job states = %v, want both canceled", states)
	}
}

func TestExpiredRunningJobRecoversAndCanBeClaimedAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recover.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Enqueue(ctx, "sync_artist", "recover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE id=?`, time.Now().Add(-time.Second).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recovered, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != id || recovered.State != "running" || recovered.Attempt != first.Attempt+1 || recovered.LeaseToken == first.LeaseToken {
		t.Fatalf("recovered job = %+v, prior lease = %+v", recovered, first)
	}
}

func TestRetryAttemptBudgetEndsInFailed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.Enqueue(ctx, "sync_artist", "budget", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	for attempt := 1; attempt <= 5; attempt++ {
		j, err := s.Claim(ctx, now.Add(time.Duration(attempt)*time.Millisecond))
		if err != nil {
			t.Fatalf("Claim attempt %d: %v", attempt, err)
		}
		if j.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", j.Attempt, attempt)
		}
		if err := s.Finish(ctx, j, "retry_wait", "TEMPORARY", now.Add(time.Duration(attempt+1)*time.Millisecond)); err != nil {
			t.Fatalf("Finish attempt %d: %v", attempt, err)
		}
	}
	jobs, err := s.Jobs(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != "failed" || jobs[0].Attempt != 5 {
		t.Fatalf("job after attempt budget = %+v, want failed at attempt 5", jobs)
	}
	if _, err := s.Claim(ctx, now.Add(time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Claim after attempt budget = %v, want ErrNotFound", err)
	}
}
