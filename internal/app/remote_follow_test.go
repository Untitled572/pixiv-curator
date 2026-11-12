package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type remoteFollowClientFunc func(context.Context, int64) error

func (f remoteFollowClientFunc) FollowArtist(ctx context.Context, artistID int64) error {
	return f(ctx, artistID)
}

func TestRemoteFollowsHandlerUsesExplicitAccountAndRechecksBeforeWriting(t *testing.T) {
	ctx := context.Background()
	store := openRemoteFollowStore(t)
	addRemoteFollowAccount(t, store, "default", 101)
	addRemoteFollowAccount(t, store, "target", 202)
	if err := store.UpsertArtist(ctx, domain.Artist{ID: 8101, Name: "pending artist"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	var clientCalls int
	var routedAccount string
	remote := NewRemoteFollows(store, remoteFollowClientFunc(func(requestCtx context.Context, artistID int64) error {
		clientCalls++
		routedAccount = domain.ContextAccount(requestCtx)
		if artistID != 8101 {
			t.Errorf("follow artist ID=%d", artistID)
		}
		return nil
	}))
	job := enqueueAppFollow(t, store, "target", 8101, "copy", "valid-copy")
	claimed := claimAppFollow(t, store, job)
	if err := remote.Handler(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if clientCalls != 1 || routedAccount != "target" {
		t.Fatalf("client calls=%d routed account=%q", clientCalls, routedAccount)
	}
	artists, err := store.Artists(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) == 0 || artists[0].ReviewStatus != "pending" || !artists[0].RemoteFollowed {
		t.Fatalf("remote follow changed local approval or missed aggregate: %+v", artists)
	}
	accounts, err := store.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[1].ID != "target" || accounts[1].FollowedArtists != 1 {
		t.Fatalf("explicit target relation not recorded: %+v", accounts)
	}

	// A duplicate task observes the stored follow and skips another API call.
	duplicate := enqueueAppFollow(t, store, "target", 8101, "copy", "duplicate-copy")
	claimedDuplicate := claimAppFollow(t, store, duplicate)
	if err := remote.Handler(ctx, claimedDuplicate); err != nil {
		t.Fatal(err)
	}
	if clientCalls != 1 {
		t.Fatalf("already-followed artist caused %d API calls", clientCalls)
	}

	for i, raw := range []json.RawMessage{json.RawMessage(`{`), json.RawMessage(`{"account_id":"target","artist_id":0,"origin":"copy"}`), json.RawMessage(`{"account_id":"target","artist_id":8101,"origin":"untrusted"}`)} {
		err := remote.Handler(ctx, domain.Job{Kind: "follow_artist", Payload: raw})
		var jobErr *jobs.JobError
		if !errors.As(err, &jobErr) || jobErr.Code != "INVALID_TASK" {
			t.Fatalf("invalid payload %d error=%v", i, err)
		}
	}
	missing := enqueueAppFollow(t, store, "missing-account", 8101, "copy", "missing-account")
	claimedMissing := claimAppFollow(t, store, missing)
	if err := remote.Handler(ctx, claimedMissing); err == nil || clientCalls != 1 {
		t.Fatalf("invalid account call err=%v API calls=%d", err, clientCalls)
	}

	if err := store.UpsertArtist(ctx, domain.Artist{ID: 8102, Name: "blocked candidate"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideArtist(ctx, 8102, "block"); err != nil {
		t.Fatal(err)
	}
	blocked := enqueueAppFollow(t, store, "target", 8102, "copy", "blocked")
	claimedBlocked := claimAppFollow(t, store, blocked)
	if err := remote.Handler(ctx, claimedBlocked); err != nil || clientCalls != 1 {
		t.Fatalf("blocked artist err=%v API calls=%d", err, clientCalls)
	}

	if err := store.UpsertArtist(ctx, domain.Artist{ID: 8103, Name: "cancel candidate"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	canceled := enqueueAppFollow(t, store, "target", 8103, "copy", "canceled")
	claimedCanceled := claimAppFollow(t, store, canceled)
	if err := store.Cancel(ctx, canceled); err != nil {
		t.Fatal(err)
	}
	err = remote.Handler(ctx, claimedCanceled)
	var jobErr *jobs.JobError
	if !errors.As(err, &jobErr) || jobErr.Class != jobs.Failed || !errors.Is(err, domain.ErrLease) || clientCalls != 1 {
		t.Fatalf("canceled job err=%v calls=%d", err, clientCalls)
	}
}

func TestRemoteFollowsHandlerClassifies429AndReportsCommitFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want jobs.ErrorClass
	}{{"upstream-retry", &pixiv.Error{Status: 429, Code: "RATE_LIMITED", RetryAfter: 3 * time.Second}, jobs.Retry}, {"local-cooldown", &pixiv.Error{Status: 429, Code: "RATE_LIMITED", RetryAfter: 3 * time.Second, LocalCooldown: true}, jobs.Deferred}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openRemoteFollowStore(t)
			addRemoteFollowAccount(t, store, "account", 301)
			if err := store.UpsertArtist(ctx, domain.Artist{ID: 8201, Name: "artist"}, "manual", "test"); err != nil {
				t.Fatal(err)
			}
			job := enqueueAppFollow(t, store, "account", 8201, "copy", tc.name)
			claimed := claimAppFollow(t, store, job)
			remote := NewRemoteFollows(store, remoteFollowClientFunc(func(context.Context, int64) error { return tc.err }))
			err := remote.Handler(ctx, claimed)
			var jobErr *jobs.JobError
			if !errors.As(err, &jobErr) || jobErr.Class != tc.want || jobErr.Code != "RATE_LIMITED" || jobErr.RetryAfter != 3*time.Second {
				t.Fatalf("handler error=%v want %s RATE_LIMITED with Retry-After", err, tc.want)
			}
		})
	}

	t.Run("commit-failure-is-not-success", func(t *testing.T) {
		ctx := context.Background()
		store := openRemoteFollowStore(t)
		addRemoteFollowAccount(t, store, "account", 401)
		if err := store.UpsertArtist(ctx, domain.Artist{ID: 8202, Name: "artist"}, "manual", "test"); err != nil {
			t.Fatal(err)
		}
		job := enqueueAppFollow(t, store, "account", 8202, "copy", "commit-failure")
		claimed := claimAppFollow(t, store, job)
		remote := NewRemoteFollows(store, remoteFollowClientFunc(func(context.Context, int64) error {
			_, err := store.DecideArtist(ctx, 8202, "block")
			return err
		}))
		err := remote.Handler(ctx, claimed)
		if err == nil {
			t.Fatal("commit failure was reported as success")
		}
		accounts, listErr := store.Accounts(ctx)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(accounts) != 1 || accounts[0].FollowedArtists != 0 {
			t.Fatalf("commit failure left account state %+v", accounts)
		}
	})
}

func openRemoteFollowStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/remote-follow.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func addRemoteFollowAccount(t *testing.T, store *sqlite.Store, id string, pixivID int64) {
	t.Helper()
	if err := store.SaveAccount(context.Background(), domain.Account{ID: id, Name: id, PixivID: pixivID, TokenFile: "fake-token-file"}); err != nil {
		t.Fatal(err)
	}
}

func enqueueAppFollow(t *testing.T, store *sqlite.Store, accountID string, artistID int64, origin, suffix string) string {
	t.Helper()
	_ = suffix
	payload, err := json.Marshal(map[string]any{"account_id": accountID, "artist_id": artistID, "origin": origin})
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.Enqueue(context.Background(), "follow_artist", fmt.Sprintf("follow:%s:%d", accountID, artistID), payload)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func claimAppFollow(t *testing.T, store *sqlite.Store, wantedID string) domain.Job {
	t.Helper()
	ctx := context.Background()
	for attempts := 0; attempts < 20; attempts++ {
		job, err := store.Claim(ctx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if job.ID == wantedID {
			return job
		}
		if err = store.Finish(ctx, job, "succeeded", "", time.Now()); err != nil {
			t.Fatalf("finish unrelated job %s: %v", job.ID, err)
		}
	}
	t.Fatalf("job %s was not claimed", wantedID)
	return domain.Job{}
}
