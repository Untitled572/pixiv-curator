package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type fakePixivClient struct {
	following func(context.Context, string) (pixiv.FollowingPage, error)
	artist    func(context.Context, int64, string) (pixiv.ArtworkPage, error)
	artwork   func(context.Context, int64) (pixiv.RawArtwork, error)
}

type fakeRestrictedPixivClient struct {
	*fakePixivClient
	restrictedFollowing func(context.Context, string, string) (pixiv.FollowingPage, error)
}

func (f *fakeRestrictedPixivClient) FollowingRestricted(ctx context.Context, cursor, visibility string) (pixiv.FollowingPage, error) {
	return f.restrictedFollowing(ctx, cursor, visibility)
}

func (f *fakePixivClient) Following(ctx context.Context, cursor string) (pixiv.FollowingPage, error) {
	return f.following(ctx, cursor)
}
func (f *fakePixivClient) ArtistArtworks(ctx context.Context, id int64, cursor string) (pixiv.ArtworkPage, error) {
	return f.artist(ctx, id, cursor)
}
func (f *fakePixivClient) Artwork(ctx context.Context, id int64) (pixiv.RawArtwork, error) {
	return f.artwork(ctx, id)
}
func (f *fakePixivClient) Status() string                            { return "ready" }
func (f *fakePixivClient) Credentials(context.Context, string) error { return nil }

func openIngestTestStore(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func claimIngestJob(t *testing.T, s *sqlite.Store, kind, key string, payload json.RawMessage) domain.Job {
	t.Helper()
	if _, err := s.Enqueue(context.Background(), kind, key, payload); err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func rawArtist(id int64, name string) pixiv.RawArtist {
	return pixiv.RawArtist{ID: id, Name: name, Account: fmt.Sprintf("artist-%d", id)}
}

func rawArtwork(id, artistID int64) pixiv.RawArtwork {
	zero, nonAI := 0, 1
	a := pixiv.RawArtwork{ID: id, User: rawArtist(artistID, "artist"), Type: "illust", PageCount: 1, Title: fmt.Sprintf("illust-%d", id), XRestrict: &zero, IllustAIType: &nonAI}
	a.MetaSinglePage.OriginalImageURL = fmt.Sprintf("https://i.pximg.net/img-original/%d.jpg", id)
	a.ImageURLs.Medium = fmt.Sprintf("https://i.pximg.net/c/600x600/img-master/%d.jpg", id)
	return a
}

func TestFollowingSnapshotEnrollsArtistsAndOnlyUnfollowsAfterFinalPage(t *testing.T) {
	ctx := context.Background()
	s, _ := openIngestTestStore(t)
	var calls []string
	client := &fakePixivClient{following: func(_ context.Context, cursor string) (pixiv.FollowingPage, error) {
		calls = append(calls, cursor)
		switch len(calls) {
		case 1:
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(101, "A"), rawArtist(102, "B")}}, nil
		case 2:
			if cursor != "" {
				t.Fatalf("new snapshot started at cursor %q", cursor)
			}
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(101, "A")}, Next: "page-2"}, nil
		case 3:
			if cursor != "page-2" {
				t.Fatalf("second page cursor=%q", cursor)
			}
			// The prior snapshot's B remains followed until this final page commits.
			artists, err := s.Artists(context.Background(), 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range artists {
				if a.ID == 102 && !a.RemoteFollowed {
					t.Fatal("artist B was marked unfollowed before final page")
				}
			}
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(103, "C")}}, nil
		default:
			t.Fatalf("unexpected following request %d", len(calls))
			return pixiv.FollowingPage{}, nil
		}
	}}
	service := NewIngest(s, client)

	first := claimIngestJob(t, s, "sync_following", "follow-run-1", json.RawMessage(`{"cursor":""}`))
	if err := service.Handlers()["sync_following"](ctx, first); err != nil {
		t.Fatal(err)
	}
	second := claimIngestJob(t, s, "sync_following", "follow-run-2", json.RawMessage(`{"cursor":""}`))
	if err := service.Handlers()["sync_following"](ctx, second); err != nil {
		t.Fatal(err)
	}

	artists, err := s.Artists(ctx, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[int64]domain.Artist, len(artists))
	for _, a := range artists {
		byID[a.ID] = a
	}
	if len(byID) != 3 {
		t.Fatalf("artist count=%d; want 3", len(byID))
	}
	for _, id := range []int64{101, 102, 103} {
		a := byID[id]
		if a.ReviewStatus != "approved" || !a.CollectionEnabled {
			t.Errorf("following import did not enroll artist %d: %+v", id, a)
		}
	}
	if byID[101].RemoteFollowed != true || byID[103].RemoteFollowed != true || byID[102].RemoteFollowed {
		t.Fatalf("final snapshot remote-followed flags: A=%v B=%v C=%v", byID[101].RemoteFollowed, byID[102].RemoteFollowed, byID[103].RemoteFollowed)
	}
}

func TestArtistPaginationContinuesPastOldArtworkAndResumesFromCommittedCursor(t *testing.T) {
	ctx := context.Background()
	s, dbPath := openIngestTestStore(t)
	catalog := NewCatalog(s)
	if err := catalog.ImportArtist(ctx, 201); err != nil {
		t.Fatal(err)
	}
	jobID, err := catalog.DecideArtist(ctx, 201, "approve")
	if err != nil {
		t.Fatal(err)
	}
	// The first returned artwork already exists locally; it must not terminate the scan.
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 9001, ArtistID: 201, PageCount: 0}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != jobID {
		t.Fatalf("claimed job=%q; want approval job %q", job.ID, jobID)
	}

	var requests []string
	failSecondPage := true
	client := &fakePixivClient{artist: func(_ context.Context, artistID int64, cursor string) (pixiv.ArtworkPage, error) {
		if artistID != 201 {
			t.Fatalf("artist request ID=%d", artistID)
		}
		requests = append(requests, cursor)
		switch cursor {
		case "":
			return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{rawArtwork(9001, 201)}, Next: "page-2"}, nil
		case "page-2":
			if failSecondPage {
				failSecondPage = false
				return pixiv.ArtworkPage{}, errors.New("temporary upstream failure")
			}
			return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{rawArtwork(9002, 201)}}, nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return pixiv.ArtworkPage{}, nil
		}
	}}
	service := NewIngest(s, client)
	handler := service.Handlers()["sync_artist"]
	if err := handler(ctx, job); err == nil {
		t.Fatal("second-page upstream failure was not returned")
	}
	cursor, err := s.ArtistCursor(ctx, 201, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "page-2" {
		t.Fatalf("persisted cursor=%q; want page-2", cursor)
	}
	// Retry the handler with the same still-live lease. It must resume at page two.
	if err := handler(ctx, job); err != nil {
		t.Fatalf("resumed ingest: %v", err)
	}
	if got := fmt.Sprint(requests); got != "[ page-2 page-2]" {
		t.Fatalf("page request cursors=%q; first-page old artwork must not stop scanning", got)
	}
	jobs, err := s.Jobs(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var syncSucceeded bool
	for _, j := range jobs {
		if j.ID == job.ID && j.State == "succeeded" {
			syncSucceeded = true
		}
	}
	if !syncSucceeded {
		t.Fatalf("artist sync job did not finish succeeded: %+v", jobs)
	}
	cursor, err = s.ArtistCursor(ctx, 201, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "" {
		t.Fatalf("final cursor=%q; want empty cursor at completed snapshot", cursor)
	}
	artists, err := s.Artists(ctx, 0, 10)
	if err != nil || len(artists) != 1 || artists[0].ReviewStatus != "approved" {
		t.Fatalf("approved artist state=%+v err=%v", artists, err)
	}
	// Both the previously known first-page item and later-page item must remain stored.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, artID := range []int64{9001, 9002} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM artworks WHERE id=? AND artist_id=?`, artID, 201).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("artwork %d count=%d; want 1", artID, count)
		}
	}
}

func TestHydrateUsesFixedIllustIDFromTask(t *testing.T) {
	ctx := context.Background()
	s, _ := openIngestTestStore(t)
	catalog := NewCatalog(s)
	if err := catalog.ImportArtist(ctx, 301); err != nil {
		t.Fatal(err)
	}
	syncID, err := catalog.DecideArtist(ctx, 301, "approve")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, syncID); err != nil {
		t.Fatal(err)
	}
	stub, err := pixiv.NormalizeArtwork(rawArtwork(7654321, 301))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, stub); err != nil {
		t.Fatal(err)
	}
	const illustID int64 = 7654321
	job := claimIngestJob(t, s, "hydrate_artwork", "illust:7654321", json.RawMessage(`{"illust_id":7654321}`))
	var requested int64
	client := &fakePixivClient{artwork: func(_ context.Context, id int64) (pixiv.RawArtwork, error) {
		requested = id
		return rawArtwork(id, 301), nil
	}}
	if err := NewIngest(s, client).Handlers()["hydrate_artwork"](ctx, job); err != nil {
		t.Fatal(err)
	}
	if requested != illustID {
		t.Fatalf("upstream artwork request ID=%d; want %d", requested, illustID)
	}
	jobs, err := s.Jobs(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var hydrationSucceeded bool
	for _, j := range jobs {
		if j.ID == job.ID && j.Kind == "hydrate_artwork" && j.State == "succeeded" {
			hydrationSucceeded = true
		}
	}
	if !hydrationSucceeded {
		t.Fatalf("hydration job did not finish succeeded: %+v", jobs)
	}
}

func TestPausedArtistDoesNotStartArtistOrHydrationFetch(t *testing.T) {
	ctx := context.Background()
	store, _ := openIngestTestStore(t)
	catalog := NewCatalog(store)
	const artistID, illustID int64 = 331, 76331
	if err := catalog.ImportArtist(ctx, artistID); err != nil {
		t.Fatal(err)
	}
	syncID, err := catalog.DecideArtist(ctx, artistID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	syncJob, err := store.Claim(ctx, time.Now())
	if err != nil || syncJob.ID != syncID {
		t.Fatalf("claim sync=%+v err=%v", syncJob, err)
	}
	artwork, err := pixiv.NormalizeArtwork(rawArtwork(illustID, artistID))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}
	hydrateJob := claimIngestJob(t, store, "hydrate_artwork", "paused-hydrate", json.RawMessage(`{"illust_id":76331}`))
	queuedHydrationID, err := store.Enqueue(ctx, "hydrate_artwork", "queued-hydrate", json.RawMessage(`{"illust_id":76331}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.SetCollection(ctx, artistID, false); err != nil {
		t.Fatal(err)
	}
	queuedHydration, err := store.Job(ctx, queuedHydrationID)
	if err != nil || queuedHydration.State != "canceled" {
		t.Fatalf("queued hydration=%+v err=%v; want canceled", queuedHydration, err)
	}
	artistCalls, hydrateCalls := 0, 0
	client := &fakePixivClient{
		artist: func(context.Context, int64, string) (pixiv.ArtworkPage, error) {
			artistCalls++
			return pixiv.ArtworkPage{}, nil
		},
		artwork: func(_ context.Context, id int64) (pixiv.RawArtwork, error) {
			hydrateCalls++
			return rawArtwork(id, artistID), nil
		},
	}
	handlers := NewIngest(store, client).Handlers()
	if err := handlers["sync_artist"](ctx, syncJob); err == nil {
		t.Fatal("paused sync unexpectedly succeeded")
	}
	if err := handlers["hydrate_artwork"](ctx, hydrateJob); err == nil {
		t.Fatal("paused hydration unexpectedly succeeded")
	}
	repairJob := claimIngestJob(t, store, "repair_media", "paused-repair", json.RawMessage(`{"illust_id":76331}`))
	if err := handlers["repair_media"](ctx, repairJob); err == nil {
		t.Fatal("repair for paused approved artist unexpectedly succeeded")
	}
	if artistCalls != 0 || hydrateCalls != 0 {
		t.Fatalf("upstream calls artist=%d hydrate=%d; want zero", artistCalls, hydrateCalls)
	}
}

func TestFollowingDefaultAvatarDoesNotInterruptSnapshot(t *testing.T) {
	ctx := context.Background()
	store, _ := openIngestTestStore(t)
	calls := 0
	client := &fakePixivClient{following: func(_ context.Context, cursor string) (pixiv.FollowingPage, error) {
		calls++
		if cursor == "" {
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(701, "first")}, Next: "last-page"}, nil
		}
		if cursor != "last-page" {
			t.Fatalf("unexpected cursor %q", cursor)
		}
		artist := rawArtist(702, "default avatar")
		artist.ProfileImageURLs.Medium = "https://s.pximg.net/common/images/no_profile.png"
		return pixiv.FollowingPage{Artists: []pixiv.RawArtist{artist}}, nil
	}}
	job := claimIngestJob(t, store, "sync_following", "default-avatar", json.RawMessage("{}"))
	if err := NewIngest(store, client).Handlers()["sync_following"](ctx, job); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Job(ctx, job.ID)
	if err != nil || saved.State != "succeeded" || saved.ProgressPages != 2 || saved.ProgressItems != 2 || calls != 2 {
		t.Fatalf("snapshot did not complete: job=%+v calls=%d error=%v", saved, calls, err)
	}
	artists, err := store.Artists(ctx, 0, 10)
	if err != nil || len(artists) != 2 {
		t.Fatalf("artists=%+v error=%v", artists, err)
	}
	for _, artist := range artists {
		if artist.ReviewStatus != "approved" || !artist.CollectionEnabled || !artist.RemoteFollowed {
			t.Fatalf("following did not enroll artist: %+v", artist)
		}
	}
}

func TestFollowingReadsPublicThenPrivateAndResumesOnlyPrivatePhase(t *testing.T) {
	ctx := context.Background()
	store, dbPath := openIngestTestStore(t)
	if err := store.SaveAccount(ctx, domain.Account{ID: "restricted-account", Name: "restricted", TokenFile: "unused"}); err != nil {
		t.Fatal(err)
	}
	if err := NewCatalog(store).ImportArtist(ctx, 899); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,last_seen_run,visibility) VALUES('restricted-account',899,1,'previous-run','public')`); err != nil {
		t.Fatal(err)
	}
	job := claimIngestJob(t, store, "sync_following", "restricted-following", json.RawMessage(`{"account_id":"restricted-account","cursor":""}`))
	calls := make([]string, 0, 5)
	privateFailure := true
	base := &fakePixivClient{}
	client := &fakeRestrictedPixivClient{fakePixivClient: base}
	client.restrictedFollowing = func(_ context.Context, cursor, visibility string) (pixiv.FollowingPage, error) {
		calls = append(calls, visibility+":"+cursor)
		switch visibility + ":" + cursor {
		case "public:":
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(801, "A"), rawArtist(802, "B")}, Next: "public-2"}, nil
		case "public:public-2":
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(802, "B"), rawArtist(803, "C")}}, nil
		case "private:":
			var stillFollowed bool
			if err := db.QueryRowContext(ctx, `SELECT followed=1 FROM account_artists WHERE account_id='restricted-account' AND artist_id=899`).Scan(&stillFollowed); err != nil || !stillFollowed {
				t.Fatalf("unseen relations were cleared before private phase completed: followed=%v err=%v", stillFollowed, err)
			}
			var payload struct {
				Restrict string `json:"restrict"`
				Cursor   string `json:"cursor"`
			}
			persisted, err := store.Job(ctx, job.ID)
			if err != nil || json.Unmarshal(persisted.Payload, &payload) != nil {
				t.Fatalf("read persisted public-to-private handoff: job=%+v err=%v", persisted, err)
			}
			if persisted.State == "succeeded" || payload.Restrict != "private" || payload.Cursor != "" {
				t.Fatalf("public phase completed or lost private handoff: job=%+v payload=%+v", persisted, payload)
			}
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(803, "C"), rawArtist(804, "D")}, Next: "private-2"}, nil
		case "private:private-2":
			if privateFailure {
				privateFailure = false
				return pixiv.FollowingPage{}, errors.New("temporary private-page failure")
			}
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(804, "D"), rawArtist(805, "E")}}, nil
		default:
			t.Fatalf("unexpected restricted request visibility=%q cursor=%q", visibility, cursor)
			return pixiv.FollowingPage{}, nil
		}
	}
	handler := NewIngest(store, client).Handlers()["sync_following"]
	accountCtx := domain.WithAccount(ctx, "restricted-account")
	if err := handler(accountCtx, job); err == nil {
		t.Fatal("private page failure was not returned")
	}
	afterFailure, err := store.Job(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Restrict string `json:"restrict"`
		Cursor   string `json:"cursor"`
	}
	if json.Unmarshal(afterFailure.Payload, &saved) != nil || saved.Restrict != "private" || saved.Cursor != "private-2" || afterFailure.State == "succeeded" {
		t.Fatalf("private retry position was not persisted: job=%+v payload=%+v", afterFailure, saved)
	}
	if err := handler(accountCtx, afterFailure); err != nil {
		t.Fatalf("resume private phase: %v", err)
	}
	wantCalls := "[public: public:public-2 private: private:private-2 private:private-2]"
	if got := fmt.Sprint(calls); got != wantCalls {
		t.Fatalf("restricted request sequence=%q; want %q", got, wantCalls)
	}
	finished, err := store.Job(ctx, job.ID)
	if err != nil || finished.State != "succeeded" || finished.ProgressPages != 4 || finished.ProgressItems != 8 {
		t.Fatalf("combined snapshot completion=%+v err=%v; want 4 committed pages and 8 returned artists", finished, err)
	}

	rows, err := db.QueryContext(ctx, `SELECT artist_id,visibility,last_seen_run FROM account_artists WHERE account_id=? ORDER BY artist_id`, "restricted-account")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[int64][2]string)
	for rows.Next() {
		var id int64
		var visibility, runID string
		if err := rows.Scan(&id, &visibility, &runID); err != nil {
			t.Fatal(err)
		}
		got[id] = [2]string{visibility, runID}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got[899] != [2]string{"public", "previous-run"} {
		t.Fatalf("unseen prior relation history=%v; want retained row from previous run", got[899])
	}
	delete(got, 899)
	wantVisibility := map[int64]string{801: "public", 802: "public", 803: "private", 804: "private", 805: "private"}
	if len(got) != len(wantVisibility) {
		t.Fatalf("unique account artist rows=%v; want %d rows", got, len(wantVisibility))
	}
	for id, visibility := range wantVisibility {
		if got[id] != [2]string{visibility, job.ID} {
			t.Errorf("artist %d relation=%v; want visibility=%s and shared run %s", id, got[id], visibility, job.ID)
		}
	}
	var oldFollowed bool
	if err := db.QueryRowContext(ctx, `SELECT followed=1 FROM account_artists WHERE account_id='restricted-account' AND artist_id=899`).Scan(&oldFollowed); err != nil || oldFollowed {
		t.Errorf("old unseen relation followed=%v err=%v after complete snapshot; want unfollowed only at final phase", oldFollowed, err)
	}
}

func TestFollowingRestrictedEmptyPrivatePageCompletesWithoutAddingItems(t *testing.T) {
	ctx := context.Background()
	store, _ := openIngestTestStore(t)
	if err := store.SaveAccount(ctx, domain.Account{ID: "empty-private", Name: "empty", TokenFile: "unused"}); err != nil {
		t.Fatal(err)
	}
	job := claimIngestJob(t, store, "sync_following", "empty-private-following", json.RawMessage(`{"account_id":"empty-private"}`))
	var calls []string
	base := &fakePixivClient{}
	client := &fakeRestrictedPixivClient{fakePixivClient: base, restrictedFollowing: func(_ context.Context, cursor, visibility string) (pixiv.FollowingPage, error) {
		calls = append(calls, visibility+":"+cursor)
		if visibility == "public" {
			return pixiv.FollowingPage{Artists: []pixiv.RawArtist{rawArtist(811, "public")}}, nil
		}
		return pixiv.FollowingPage{}, nil
	}}
	if err := NewIngest(store, client).Handlers()["sync_following"](domain.WithAccount(ctx, "empty-private"), job); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Job(ctx, job.ID)
	if err != nil || finished.State != "succeeded" || finished.ProgressPages != 2 || finished.ProgressItems != 1 {
		t.Fatalf("empty private page should complete one page without adding items: job=%+v err=%v", finished, err)
	}
	if got := fmt.Sprint(calls); got != "[public: private:]" {
		t.Fatalf("empty private request sequence=%q", got)
	}
}

func TestFollowingRejectsInvalidVisibility(t *testing.T) {
	ctx := context.Background()
	store, _ := openIngestTestStore(t)
	called := false
	client := &fakePixivClient{following: func(context.Context, string) (pixiv.FollowingPage, error) {
		called = true
		return pixiv.FollowingPage{}, nil
	}}
	job := claimIngestJob(t, store, "sync_following", "invalid-following-phase", json.RawMessage(`{"restrict":"internal"}`))
	err := NewIngest(store, client).Handlers()["sync_following"](ctx, job)
	var jobErr *jobs.JobError
	if !errors.As(err, &jobErr) || jobErr.Code != "INVALID_TASK" || called {
		t.Fatalf("invalid visibility error=%v called=%v; want INVALID_TASK before upstream request", err, called)
	}
}
