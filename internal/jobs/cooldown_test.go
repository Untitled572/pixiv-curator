package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestRunJobDefersCooldownAtAttemptLimitButFailsOrdinaryRetry(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		err       error
		wantState string
		wantCode  string
		wantDelay time.Duration
	}{
		{
			name:      "cooldown deferred at attempt limit",
			err:       &JobError{Class: Deferred, Code: "RATE_LIMITED", RetryAfter: 45 * time.Second},
			wantState: "deferred",
			wantCode:  "RATE_LIMITED",
			wantDelay: 45 * time.Second,
		},
		{
			name:      "ordinary retry fails at attempt limit",
			err:       &JobError{Class: Retry, Code: "RATE_LIMITED", RetryAfter: 45 * time.Second},
			wantState: "failed",
			wantCode:  "RATE_LIMITED",
			wantDelay: 45 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepository{finished: make(chan finishCall, 1)}
			job := testJob()
			job.Attempt = 5
			w := NewWorker(repo, map[string]Handler{"sync_artist": func(context.Context, domain.Job) error {
				return tt.err
			}}, Config{Now: func() time.Time { return now }})

			w.runJob(context.Background(), job)
			select {
			case got := <-repo.finished:
				if got.state != tt.wantState || got.code != tt.wantCode {
					t.Fatalf("Finish = %+v, want state/code %q/%q", got, tt.wantState, tt.wantCode)
				}
				if delay := got.when.Sub(now); delay != tt.wantDelay {
					t.Fatalf("available delay = %s, want %s", delay, tt.wantDelay)
				}
			case <-time.After(time.Second):
				t.Fatal("runJob did not finish the job")
			}
		})
	}
}
