package app

import (
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"testing"
	"time"
)

func TestClassifyLocalCooldownDoesNotSpendUpstreamAttempt(t *testing.T) {
	for _, local := range []bool{false, true} {
		var got *jobs.JobError
		if !errors.As(classify(&pixiv.Error{Code: "RATE_LIMITED", Status: 429, RetryAfter: 17 * time.Second, LocalCooldown: local}), &got) {
			t.Fatal("missing classified error")
		}
		want := jobs.Retry
		if local {
			want = jobs.Deferred
		}
		if got.Class != want || got.Code != "RATE_LIMITED" || got.RetryAfter != 17*time.Second {
			t.Fatalf("local=%v classified=%+v", local, got)
		}
	}
}
