package accounts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type accountOptionsFollowTransport struct {
	oauth  http.RoundTripper
	mu     sync.Mutex
	writes []struct {
		authorization string
		artistID      string
	}
}

func (t *accountOptionsFollowTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "app-api.pixiv.net" && r.URL.Path == "/v1/user/follow/add" {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		t.mu.Lock()
		t.writes = append(t.writes, struct {
			authorization string
			artistID      string
		}{r.Header.Get("Authorization"), form.Get("user_id")})
		t.mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":false}`))}, nil
	}
	return t.oauth.RoundTrip(r)
}

func TestManagerOptionsRoutingRestartAndExplicitCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	transport := &accountOptionsFollowTransport{oauth: fakeOAuth(map[string]int64{"manager-one": 501, "manager-two": 502})}
	oauth := pixiv.OAuthConfig{ClientID: "test-client", ClientSecret: "test-secret", HashSecret: "test-hash"}
	manager, err := Open(ctx, store, filepath.Join(dir, "private"), "", oauth, transport)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Save(ctx, "", "Source", "manager-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Save(ctx, "", "Target", "manager-two")
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := manager.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != first.ID || !accounts[0].IsDefault || accounts[1].IsDefault || accounts[0].FollowOnApprove || accounts[1].FollowOnApprove {
		t.Fatalf("initial manager options/order=%+v", accounts)
	}

	if err = store.UpsertArtist(ctx, domain.Artist{ID: 9501, Name: "source follow"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	sourceJob := claimAccountOptionJob(t, store, "following:"+first.ID)
	followPayload, _ := json.Marshal(map[string]string{"account_id": first.ID, "cursor": "", "restrict": "private"})
	sourceCtx := domain.WithFollowVisibility(domain.WithAccount(ctx, first.ID), "private")
	if err = store.CommitFollowingBatch(sourceCtx, sourceJob, "source-run", []domain.Artist{{ID: 9501, Name: "source follow"}}, followPayload, true); err != nil {
		t.Fatal(err)
	}
	queued, err := manager.CopyFollowing(ctx, first.ID, second.ID)
	if err != nil || queued != 1 {
		t.Fatalf("explicit copy target queued=%d err=%v", queued, err)
	}
	transport.mu.Lock()
	writesAfterCopy := len(transport.writes)
	transport.mu.Unlock()
	if writesAfterCopy != 0 {
		t.Fatalf("CopyFollowing made %d direct Pixiv write requests; it should only queue explicit target work", writesAfterCopy)
	}
	jobs, err := store.Jobs(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var foundCopy bool
	for _, job := range jobs {
		if job.Kind != "follow_artist" {
			continue
		}
		var payload struct {
			AccountID string `json:"account_id"`
			ArtistID  int64  `json:"artist_id"`
			Origin    string `json:"origin"`
		}
		if json.Unmarshal(job.Payload, &payload) == nil && payload.AccountID == second.ID && payload.ArtistID == 9501 && payload.Origin == "copy" {
			foundCopy = true
		}
	}
	if !foundCopy {
		t.Fatal("CopyFollowing did not queue the explicitly selected target account")
	}

	followOff := false
	if err = manager.Configure(ctx, second.ID, true, &followOff); err != nil {
		t.Fatal(err)
	}
	if err = manager.FollowArtist(ctx, 9601); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	firstWrite := transport.writes[len(transport.writes)-1]
	transport.mu.Unlock()
	if firstWrite.authorization != "Bearer access-manager-two" || firstWrite.artistID != "9601" {
		t.Fatalf("configured default did not route immediately: %+v", firstWrite)
	}

	listed, err := manager.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != second.ID || !listed[0].IsDefault || listed[0].FollowOnApprove {
		t.Fatalf("updated options/order=%+v", listed)
	}
	restarted, err := Open(ctx, store, filepath.Join(dir, "private"), "", oauth, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.FollowArtist(ctx, 9602); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	lastWrite := transport.writes[len(transport.writes)-1]
	totalWrites := len(transport.writes)
	transport.mu.Unlock()
	if lastWrite.authorization != "Bearer access-manager-two" || lastWrite.artistID != "9602" || totalWrites != 2 {
		t.Fatalf("restart routing/options changed: writes=%+v", transport.writes)
	}
	listed, err = restarted.List(ctx)
	if err != nil || len(listed) != 2 || !listed[0].IsDefault || listed[0].FollowOnApprove {
		t.Fatalf("reopened options=%+v err=%v", listed, err)
	}
}

func claimAccountOptionJob(t *testing.T, store *sqlite.Store, key string) domain.Job {
	t.Helper()
	ctx := context.Background()
	for attempts := 0; attempts < 20; attempts++ {
		job, err := store.Claim(ctx, time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if job.Key == key {
			return job
		}
		if err = store.Finish(ctx, job, "succeeded", "", time.Now()); err != nil {
			t.Fatalf("finish unrelated job %s: %v", job.Key, err)
		}
	}
	t.Fatalf("job %q was not claimed", key)
	return domain.Job{}
}
