package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type fakeRepository struct {
	mu       sync.Mutex
	job      domain.Job
	claimErr error
	claims   int
	renewErr error
	renewed  chan struct{}
	finished chan finishCall
}

type finishCall struct {
	state string
	code  string
	when  time.Time
}

func (f *fakeRepository) Claim(_ context.Context, _ time.Time) (domain.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if f.claimErr != nil {
		return domain.Job{}, f.claimErr
	}
	if f.job.ID == "" {
		return domain.Job{}, domain.ErrNotFound
	}
	j := f.job
	f.job = domain.Job{}
	return j, nil
}

func (f *fakeRepository) Renew(_ context.Context, _ domain.Job, _ time.Time) error {
	if f.renewed != nil {
		select {
		case f.renewed <- struct{}{}:
		default:
		}
	}
	return f.renewErr
}

func (f *fakeRepository) Finish(_ context.Context, _ domain.Job, state, code string, when time.Time) error {
	if f.finished != nil {
		f.finished <- finishCall{state: state, code: code, when: when}
	}
	return nil
}

func testJob() domain.Job { return domain.Job{ID: "job-1", Kind: "sync_artist", Attempt: 1} }

func TestRunCancellationPropagatesToHandlerAndWaits(t *testing.T) {
	repo := &fakeRepository{job: testJob()}
	started := make(chan struct{})
	stopped := make(chan struct{})
	w := NewWorker(repo, map[string]Handler{"sync_artist": func(ctx context.Context, _ domain.Job) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}, Config{PollInterval: time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler did not receive shutdown cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not wait for handler to exit")
	}
}

func TestRenewFailureCancelsAndWaitsForHandler(t *testing.T) {
	repo := &fakeRepository{job: testJob(), renewErr: domain.ErrLease, renewed: make(chan struct{}, 1), finished: make(chan finishCall, 1)}
	started := make(chan struct{})
	stopped := make(chan struct{})
	w := NewWorker(repo, map[string]Handler{"sync_artist": func(ctx context.Context, _ domain.Job) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}, Config{RenewInterval: 5 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	<-started
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel handler")
	}
	select {
	case <-repo.renewed:
	case <-time.After(time.Second):
		t.Fatal("lease was not renewed")
	}
	select {
	case call := <-repo.finished:
		t.Fatalf("lost lease must not finish job, got %+v", call)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunPollsWithoutBusyLoop(t *testing.T) {
	repo := &fakeRepository{claimErr: domain.ErrNotFound}
	w := NewWorker(repo, nil, Config{PollInterval: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(75 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	repo.mu.Lock()
	claims := repo.claims
	repo.mu.Unlock()
	if claims < 2 || claims > 6 {
		t.Fatalf("Claim called %d times; expected bounded polling", claims)
	}
}

func TestOutcomeClassifiesErrorsAndHonorsRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	w := NewWorker(&fakeRepository{}, nil, Config{Now: func() time.Time { return now }, Jitter: func(d time.Duration) time.Duration { return d }})
	tests := []struct {
		name, wantState, wantCode string
		err                       error
		wantDelay                 time.Duration
	}{
		{name: "retry", wantState: "retry_wait", wantCode: "UPSTREAM_BUSY", err: &JobError{Class: Retry, Code: "UPSTREAM_BUSY"}, wantDelay: 2 * time.Second},
		{name: "retry-after", wantState: "retry_wait", wantCode: "RATE_LIMIT", err: &JobError{Class: Retry, Code: "RATE_LIMIT", RetryAfter: 30 * time.Second}, wantDelay: 30 * time.Second},
		{name: "auth", wantState: "paused_auth", wantCode: "TOKEN_EXPIRED", err: &JobError{Class: PausedAuth, Code: "TOKEN_EXPIRED"}},
		{name: "failed", wantState: "failed", wantCode: "INVALID_DATA", err: &JobError{Class: Failed, Code: "INVALID_DATA"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, code, available := w.outcome(testJob(), tt.err)
			if state != tt.wantState || code != tt.wantCode {
				t.Fatalf("outcome = (%q, %q), want (%q, %q)", state, code, tt.wantState, tt.wantCode)
			}
			if got := available.Sub(now); got != tt.wantDelay {
				t.Fatalf("delay = %s, want %s", got, tt.wantDelay)
			}
		})
	}
}

func TestUnsupportedJobIsFailedWithStableCode(t *testing.T) {
	repo := &fakeRepository{job: testJob(), finished: make(chan finishCall, 1)}
	repo.job.Kind = "future_kind"
	w := NewWorker(repo, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case got := <-repo.finished:
		if got.state != "failed" || got.code != "UNSUPPORTED_JOB" {
			t.Fatalf("Finish = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("unsupported job was not finished")
	}
	cancel()
	<-done
}
