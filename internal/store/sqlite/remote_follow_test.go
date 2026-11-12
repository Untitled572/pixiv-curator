package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestQueueFollowCopyValidatesSnapshotsFiltersAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "source", 101)
	createRemoteFollowAccount(t, s, "target", 202)
	createRemoteFollowAccount(t, s, "unverified", 0)
	for _, artist := range []struct {
		id     int64
		status string
	}{{1001, "approved"}, {1002, "pending"}, {1003, "blocked"}, {1004, "rejected"}, {1005, "approved"}} {
		seedArtist(t, s, artist.id)
		if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status=? WHERE id=?`, artist.status, artist.id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES('source',?,1)`, artist.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE account_artists SET visibility='private' WHERE account_id='source' AND artist_id=1005`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES('target',1001,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,visibility) VALUES('target',1005,1,'public')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='succeeded',payload_json=json_set(payload_json,'$.restrict','private') WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')='source'`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.QueueFollowCopy(ctx, "source", "source"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("same-account copy error=%v", err)
	}
	if _, err := s.QueueFollowCopy(ctx, "source", "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing target error=%v", err)
	}
	if _, err := s.QueueFollowCopy(ctx, "source", "unverified"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unverified target error=%v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='pending' WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')='target'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueFollowCopy(ctx, "target", "source"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unsynced source error=%v", err)
	}

	queued, err := s.QueueFollowCopy(ctx, "source", "target")
	if err != nil || queued != 2 {
		t.Fatalf("queued=%d err=%v, want 2 eligible artists", queued, err)
	}
	queued, err = s.QueueFollowCopy(ctx, "source", "target")
	if err != nil || queued != 0 {
		t.Fatalf("repeat queued=%d err=%v, want no duplicate tasks", queued, err)
	}
	jobs, err := s.Jobs(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]remoteFollowPayload{}
	for _, job := range jobs {
		if job.Kind != "follow_artist" {
			continue
		}
		var payload remoteFollowPayload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		got[payload.ArtistID] = payload
		if payload.AccountID != "target" || payload.Origin != "copy" || job.Key != remoteFollowKey("target", payload.ArtistID) {
			t.Fatalf("copy job=%+v payload=%+v", job, payload)
		}
	}
	if len(got) != 2 || got[1002].AccountID == "" || got[1005].AccountID == "" {
		t.Fatalf("copy targets=%+v", got)
	}
	if got[1002].Restrict != "public" || got[1005].Restrict != "private" {
		t.Fatalf("copy visibility payloads=%+v", got)
	}
}

func TestRemoteFollowNeedsCommitAndFencesPolicyChanges(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "account", 303)
	followOn := true
	if err := s.ConfigureAccount(ctx, "account", false, &followOn); err != nil {
		t.Fatal(err)
	}
	seedArtist(t, s, 2001)
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 2001, Name: "recommended"}, "recommend", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=2001`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='succeeded' WHERE kind='sync_following'`); err != nil {
		t.Fatal(err)
	}

	job := insertRemoteFollowJob(t, s, "account", 2001, "approval", "approval-success")
	claimed := claimRemoteFollowJob(t, s, job)
	if _, err := s.NeedsRemoteFollow(ctx, claimed, "account", 0, "approval"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid artist ID error=%v", err)
	}
	if _, err := s.NeedsRemoteFollow(ctx, claimed, "account", 2001, "untrusted"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid origin error=%v", err)
	}
	if _, err := s.NeedsRemoteFollow(ctx, claimed, "other", 2001, "approval"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("payload/account mismatch error=%v", err)
	}
	needs, err := s.NeedsRemoteFollow(ctx, claimed, "account", 2001, "approval")
	if err != nil || !needs {
		t.Fatalf("approval eligibility needs=%t err=%v", needs, err)
	}
	if err = s.CommitRemoteFollow(ctx, claimed, "account", 2001, "approval"); err != nil {
		t.Fatal(err)
	}
	assertRemoteFollowState(t, s, claimed.ID, "account", 2001, true)
	var followedAt int64
	if err = s.db.QueryRowContext(ctx, `SELECT followed_at FROM account_artists WHERE account_id='account' AND artist_id=2001`).Scan(&followedAt); err != nil || followedAt == 0 {
		t.Fatalf("approval followed_at=%d err=%v", followedAt, err)
	}
	var remote bool
	if err = s.db.QueryRowContext(ctx, `SELECT remote_followed FROM artists WHERE id=2001`).Scan(&remote); err != nil || !remote {
		t.Fatalf("remote_followed=%t err=%v", remote, err)
	}

	seedArtist(t, s, 2007)
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=2007`); err != nil {
		t.Fatal(err)
	}
	manualApproval := insertRemoteFollowJob(t, s, "account", 2007, "approval", "approval-manual-source")
	claimedManual := claimRemoteFollowJob(t, s, manualApproval)
	if needs, needsErr := s.NeedsRemoteFollow(ctx, claimedManual, "account", 2007, "approval"); needsErr != nil || needs {
		t.Fatalf("manual-source approval needs=%t err=%v", needs, needsErr)
	}
	if err = s.CommitRemoteFollow(ctx, claimedManual, "account", 2007, "approval"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("manual-source approval commit error=%v, want conflict", err)
	}

	seedArtist(t, s, 2002)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,visibility) VALUES('account',2002,1,'public')`); err != nil {
		t.Fatal(err)
	}
	copyJob := insertRemoteFollowJobWithRestrict(t, s, "account", 2002, "copy", "private", "copy-success")
	claimedCopy := claimRemoteFollowJob(t, s, copyJob)
	if needs, err = s.NeedsRemoteFollow(ctx, claimedCopy, "account", 2002, "copy"); err != nil || !needs {
		t.Fatalf("copy eligibility needs=%t err=%v", needs, err)
	}
	if err = s.CommitRemoteFollow(ctx, claimedCopy, "account", 2002, "copy"); err != nil {
		t.Fatal(err)
	}
	assertRemoteFollowState(t, s, claimedCopy.ID, "account", 2002, true)
	if err = s.db.QueryRowContext(ctx, `SELECT followed_at FROM account_artists WHERE account_id='account' AND artist_id=2002`).Scan(&followedAt); err != nil || followedAt != 0 {
		t.Fatalf("copy inserted followed_at=%d err=%v", followedAt, err)
	}
	var visibility string
	if err = s.db.QueryRowContext(ctx, `SELECT visibility FROM account_artists WHERE account_id='account' AND artist_id=2002`).Scan(&visibility); err != nil || visibility != "private" {
		t.Fatalf("copy visibility=%q err=%v", visibility, err)
	}
	accounts, err := s.Accounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].PrivateFollowedArtists != 1 {
		t.Fatalf("private followed count=%+v err=%v", accounts, err)
	}
	seedArtist(t, s, 2008)
	legacyPublicJob := insertRemoteFollowJob(t, s, "account", 2008, "copy", "legacy-public-payload")
	if _, err = s.db.ExecContext(ctx, `UPDATE jobs SET payload_json=json_remove(payload_json,'$.restrict') WHERE id=?`, legacyPublicJob); err != nil {
		t.Fatal(err)
	}
	claimedLegacy := claimRemoteFollowJob(t, s, legacyPublicJob)
	if needs, needsErr := s.NeedsRemoteFollow(ctx, claimedLegacy, "account", 2008, "copy"); needsErr != nil || !needs {
		t.Fatalf("legacy missing-restrict payload needs=%t err=%v", needs, needsErr)
	}
	if err = s.CommitRemoteFollow(ctx, claimedLegacy, "account", 2008, "copy"); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT visibility FROM account_artists WHERE account_id='account' AND artist_id=2008`).Scan(&visibility); err != nil || visibility != "public" {
		t.Fatalf("legacy task did not default to public: visibility=%q err=%v", visibility, err)
	}
	seedArtist(t, s, 2006)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,followed_at) VALUES('account',2006,0,123)`); err != nil {
		t.Fatal(err)
	}
	existingFollowTime := insertRemoteFollowJob(t, s, "account", 2006, "copy", "copy-existing-time")
	claimedExisting := claimRemoteFollowJob(t, s, existingFollowTime)
	if err = s.CommitRemoteFollow(ctx, claimedExisting, "account", 2006, "copy"); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT followed_at FROM account_artists WHERE account_id='account' AND artist_id=2006`).Scan(&followedAt); err != nil || followedAt != 123 {
		t.Fatalf("copy replaced existing followed_at=%d err=%v", followedAt, err)
	}

	seedArtist(t, s, 2003)
	blockedJob := insertRemoteFollowJob(t, s, "account", 2003, "copy", "blocked")
	claimedBlocked := claimRemoteFollowJob(t, s, blockedJob)
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET review_status='blocked' WHERE id=2003`); err != nil {
		t.Fatal(err)
	}
	if needs, err = s.NeedsRemoteFollow(ctx, claimedBlocked, "account", 2003, "copy"); err != nil || needs {
		t.Fatalf("blocked artist needs=%t err=%v", needs, err)
	}
	if err = s.CommitRemoteFollow(ctx, claimedBlocked, "account", 2003, "copy"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("blocked commit error=%v", err)
	}

	seedArtist(t, s, 2004)
	canceledJob := insertRemoteFollowJob(t, s, "account", 2004, "copy", "canceled")
	claimedCanceled := claimRemoteFollowJob(t, s, canceledJob)
	if err = s.Cancel(ctx, claimedCanceled.ID); err != nil {
		t.Fatal(err)
	}
	if needs, err = s.NeedsRemoteFollow(ctx, claimedCanceled, "account", 2004, "copy"); !errors.Is(err, domain.ErrLease) || needs {
		t.Fatalf("canceled lease needs=%t err=%v", needs, err)
	}
	if err = s.CommitRemoteFollow(ctx, claimedCanceled, "account", 2004, "copy"); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("canceled commit error=%v", err)
	}

	seedArtist(t, s, 2005)
	staleJob := insertRemoteFollowJob(t, s, "account", 2005, "copy", "stale")
	claimedStale := claimRemoteFollowJob(t, s, staleJob)
	if _, err = s.db.ExecContext(ctx, `UPDATE jobs SET lease_until=0 WHERE id=?`, claimedStale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NeedsRemoteFollow(ctx, claimedStale, "account", 2005, "copy"); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("stale lease needs error=%v", err)
	}
	if err = s.CommitRemoteFollow(ctx, claimedStale, "account", 2005, "copy"); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("stale lease commit error=%v", err)
	}
}

func TestApproveQueuesOnlyDefaultOptedInAndDisablingCancelsOnlyApproval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "default", 401)
	createRemoteFollowAccount(t, s, "other", 402)
	followOn := true
	if err := s.ConfigureAccount(ctx, "default", false, &followOn); err != nil {
		t.Fatal(err)
	}
	seedArtist(t, s, 3001)
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 3001, Name: "recommended"}, "recommend", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 3001, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3001); got != 1 {
		t.Fatalf("pending approval queued %d follow jobs, want one", got)
	}
	var payload remoteFollowPayload
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM jobs WHERE kind='follow_artist' AND json_extract(payload_json,'$.artist_id')=3001`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.AccountID != "default" || payload.Origin != "approval" || payload.ArtistID != 3001 {
		t.Fatalf("approval payload=%+v err=%v", payload, err)
	}
	if _, err := s.DecideArtist(ctx, 3001, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3001); got != 1 {
		t.Fatalf("repeat approval queued %d follow jobs", got)
	}

	seedArtist(t, s, 3002)
	runningApproval := insertRemoteFollowJob(t, s, "default", 3002, "approval", "approval-running")
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='running',lease_token='lease-active',lease_until=? WHERE id=?`, time.Now().Add(time.Minute).UnixMilli(), runningApproval); err != nil {
		t.Fatal(err)
	}
	copyJob := insertRemoteFollowJob(t, s, "other", 3002, "copy", "copy-stays")
	followOff := false
	if err := s.ConfigureAccount(ctx, "default", false, &followOff); err != nil {
		t.Fatal(err)
	}
	var approvalState, copyState string
	var approvalCanceled bool
	if err := s.db.QueryRowContext(ctx, `SELECT state,cancel_requested FROM jobs WHERE id=?`, runningApproval).Scan(&approvalState, &approvalCanceled); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, copyJob).Scan(&copyState); err != nil {
		t.Fatal(err)
	}
	if approvalState != "running" || !approvalCanceled || copyState != "pending" {
		t.Fatalf("disable cancellation: approval=%s canceled=%t copy=%s", approvalState, approvalCanceled, copyState)
	}
	var canceledPending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='follow_artist' AND json_extract(payload_json,'$.account_id')='default' AND json_extract(payload_json,'$.origin')='approval' AND state='canceled'`).Scan(&canceledPending); err != nil || canceledPending != 1 {
		t.Fatalf("pending approval cancellations=%d err=%v", canceledPending, err)
	}
	seedArtist(t, s, 3003)
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 3003, Name: "recommended"}, "recommend", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 3003, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3003); got != 0 {
		t.Fatalf("approval after opt-out queued %d follow jobs", got)
	}
}

func TestApproveRequiresOptInAndRecommendationSource(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "default", 403)

	seedArtist(t, s, 3010)
	if _, err := s.DecideArtist(ctx, 3010, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3010); got != 0 {
		t.Fatalf("default-off account queued %d follows", got)
	}

	followOn := true
	if err := s.ConfigureAccount(ctx, "default", false, &followOn); err != nil {
		t.Fatal(err)
	}
	seedArtist(t, s, 3011)
	if _, err := s.DecideArtist(ctx, 3011, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3011); got != 0 {
		t.Fatalf("manual approval queued %d follows", got)
	}

	seedArtist(t, s, 3012)
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 3012, Name: "recommended"}, "recommend", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideArtist(ctx, 3012, "approve"); err != nil {
		t.Fatal(err)
	}
	if got := countRemoteFollowJobs(t, s, 3012); got != 1 {
		t.Fatalf("opted-in recommendation queued %d follows, want 1", got)
	}
}

func TestQueueFollowCopyRequiresPrivateSnapshotAndReplacesConflictingVisibilityJob(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "old-source", 420)
	createRemoteFollowAccount(t, s, "source", 421)
	createRemoteFollowAccount(t, s, "target", 422)
	seedArtist(t, s, 3030)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,visibility) VALUES('old-source',3030,1,'public'),('source',3030,1,'private'),('target',3030,1,'public')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='succeeded',payload_json=json_remove(payload_json,'$.restrict') WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')='old-source'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueFollowCopy(ctx, "old-source", "target"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("public-only source copy error=%v, want conflict", err)
	}
	old := insertRemoteFollowJobWithRestrict(t, s, "target", 3030, "copy", "public", "conflicting-public")
	queued, err := s.QueueFollowCopy(ctx, "source", "target")
	if err != nil || queued != 1 {
		t.Fatalf("visibility correction queued=%d err=%v, want 1", queued, err)
	}
	var oldState string
	if err = s.db.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, old).Scan(&oldState); err != nil || oldState != "canceled" {
		t.Fatalf("conflicting public job state=%q err=%v", oldState, err)
	}
	var newPayload string
	if err = s.db.QueryRowContext(ctx, `SELECT payload_json FROM jobs WHERE kind='follow_artist' AND state='pending' AND json_extract(payload_json,'$.account_id')='target' AND json_extract(payload_json,'$.artist_id')=3030`).Scan(&newPayload); err != nil {
		t.Fatal(err)
	}
	var payload remoteFollowPayload
	if err = json.Unmarshal([]byte(newPayload), &payload); err != nil || payload.Restrict != "private" {
		t.Fatalf("replacement job payload=%+v err=%v", payload, err)
	}
	queued, err = s.QueueFollowCopy(ctx, "source", "target")
	if err != nil || queued != 0 {
		t.Fatalf("repeated visibility correction queued=%d err=%v, want 0", queued, err)
	}
	seedArtist(t, s, 3031)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed,visibility) VALUES('source',3031,1,'public')`); err != nil {
		t.Fatal(err)
	}
	approval := insertRemoteFollowJobWithRestrict(t, s, "target", 3031, "approval", "public", "approval-to-copy")
	queued, err = s.QueueFollowCopy(ctx, "source", "target")
	if err != nil || queued != 1 {
		t.Fatalf("explicit copy did not replace approval intent: queued=%d err=%v", queued, err)
	}
	var approvalState string
	if err = s.db.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, approval).Scan(&approvalState); err != nil || approvalState != "canceled" {
		t.Fatalf("replaced approval state=%q err=%v", approvalState, err)
	}
}

func TestBlockAndRejectCancelBothRemoteFollowOrigins(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"block", "reject"} {
		t.Run(action, func(t *testing.T) {
			s := openTestStore(t)
			createRemoteFollowAccount(t, s, "copy-account", 411)
			createRemoteFollowAccount(t, s, "approval-account", 412)
			artistID := int64(3020)
			if action == "reject" {
				artistID++
			}
			seedArtist(t, s, artistID)
			copyID := insertRemoteFollowJob(t, s, "copy-account", artistID, "copy", action+"-copy")
			approvalID := insertRemoteFollowJob(t, s, "approval-account", artistID, "approval", action+"-approval")
			if _, err := s.DecideArtist(ctx, artistID, action); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{copyID, approvalID} {
				var state string
				var canceled bool
				if err := s.db.QueryRowContext(ctx, `SELECT state,cancel_requested FROM jobs WHERE id=?`, id).Scan(&state, &canceled); err != nil {
					t.Fatal(err)
				}
				if state != "canceled" || !canceled {
					t.Fatalf("%s job state=%s canceled=%t", id, state, canceled)
				}
			}
		})
	}
}

func TestRemoteFollowCommitRejectsDeletedAccountAndAlreadyFollowedNeedsNoCall(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createRemoteFollowAccount(t, s, "account", 505)
	seedArtist(t, s, 4001)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES('account',4001,1)`); err != nil {
		t.Fatal(err)
	}
	alreadyJob := insertRemoteFollowJob(t, s, "account", 4001, "copy", "already-followed")
	claimedAlready := claimRemoteFollowJob(t, s, alreadyJob)
	if needs, err := s.NeedsRemoteFollow(ctx, claimedAlready, "account", 4001, "copy"); err != nil || needs {
		t.Fatalf("already followed needs=%t err=%v", needs, err)
	}

	seedArtist(t, s, 4002)
	deletedJob := insertRemoteFollowJob(t, s, "account", 4002, "copy", "deleted-account")
	claimedDeleted := claimRemoteFollowJob(t, s, deletedJob)
	if err := s.DeleteAccount(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	if needs, err := s.NeedsRemoteFollow(ctx, claimedDeleted, "account", 4002, "copy"); !errors.Is(err, domain.ErrLease) || needs {
		t.Fatalf("deleted-account lease needs=%t err=%v", needs, err)
	}
	if err := s.CommitRemoteFollow(ctx, claimedDeleted, "account", 4002, "copy"); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("deleted-account commit error=%v", err)
	}
	var followed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_artists WHERE account_id='account' AND artist_id=4002 AND followed=1`).Scan(&followed); err != nil || followed != 0 {
		t.Fatalf("deleted account relation followed=%d err=%v", followed, err)
	}
}

func createRemoteFollowAccount(t *testing.T, s *Store, id string, pixivID int64) {
	t.Helper()
	if err := s.SaveAccount(context.Background(), domain.Account{ID: id, Name: id, PixivID: pixivID, TokenFile: "test-token-file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET state='succeeded',error_code='',payload_json=json_set(payload_json,'$.restrict','private') WHERE kind='sync_following' AND json_extract(payload_json,'$.account_id')=?`, id); err != nil {
		t.Fatal(err)
	}
}

func insertRemoteFollowJob(t *testing.T, s *Store, accountID string, artistID int64, origin, key string) string {
	return insertRemoteFollowJobWithRestrict(t, s, accountID, artistID, origin, "public", key)
}

func insertRemoteFollowJobWithRestrict(t *testing.T, s *Store, accountID string, artistID int64, origin, restrict, key string) string {
	t.Helper()
	payload, err := json.Marshal(remoteFollowPayload{AccountID: accountID, ArtistID: artistID, Origin: origin, Restrict: restrict})
	if err != nil {
		t.Fatal(err)
	}
	id := "test-" + key
	now := time.Now().UnixMilli()
	if _, err = s.db.Exec(`INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at) VALUES(?,'follow_artist',?,?,'pending',?,?,?)`, id, remoteFollowKey(accountID, artistID), string(payload), now, now, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func claimRemoteFollowJob(t *testing.T, s *Store, id string) domain.Job {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE jobs SET available_at=CASE WHEN id=? THEN 0 ELSE 9999999999999 END WHERE state IN ('pending','retry_wait')`, id); err != nil {
		t.Fatal(err)
	}
	j, err := s.Claim(context.Background(), time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != id {
		t.Fatalf("claimed job=%s want %s", j.ID, id)
	}
	return j
}

func assertRemoteFollowState(t *testing.T, s *Store, jobID, accountID string, artistID int64, succeeded bool) {
	t.Helper()
	var followed bool
	var state, code string
	if err := s.db.QueryRow(`SELECT followed FROM account_artists WHERE account_id=? AND artist_id=?`, accountID, artistID).Scan(&followed); err != nil || !followed {
		t.Fatalf("remote relation followed=%t err=%v", followed, err)
	}
	if err := s.db.QueryRow(`SELECT state,error_code FROM jobs WHERE id=?`, jobID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	want := "running"
	if succeeded {
		want = "succeeded"
	}
	if state != want || code != "" {
		t.Fatalf("remote follow job state=%q error=%q want %q", state, code, want)
	}
}

func countRemoteFollowJobs(t *testing.T, s *Store, artistID int64) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='follow_artist' AND json_extract(payload_json,'$.artist_id')=?`, artistID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
