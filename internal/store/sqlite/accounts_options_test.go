package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestAccountOptionsMigrationPreservesLegacyRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 7; version++ {
		name := fmt.Sprintf("%03d_", version)
		entries, readErr := migrations.ReadDir("migrations")
		if readErr != nil {
			t.Fatal(readErr)
		}
		var migration string
		for _, entry := range entries {
			if len(entry.Name()) >= len(name) && entry.Name()[:len(name)] == name {
				migration = entry.Name()
				break
			}
		}
		if migration == "" {
			t.Fatalf("migration %d not found", version)
		}
		body, readErr := migrations.ReadFile("migrations/" + migration)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
		if _, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, int64(version)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO artists(id,name,created_at,updated_at) VALUES(501,'legacy artist',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO accounts(id,name,pixiv_id,token_file,deleted,created_at,updated_at) VALUES
	 ('older','Older',101,'legacy-old',0,10,11),
	 ('newer','Newer',102,'legacy-new',0,20,21),
	 ('deleted','Deleted',103,'legacy-deleted',1,1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO account_artists(account_id,artist_id,followed,last_seen_run) VALUES('older',501,1,'old-run')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	accounts, err := s.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != "older" || !accounts[0].IsDefault || accounts[0].PixivID != 101 || accounts[0].TokenFile != "legacy-old" || accounts[0].FollowedArtists != 1 || accounts[0].PrivateFollowedArtists != 0 || accounts[1].IsDefault {
		t.Fatalf("legacy rows/default migration = %+v", accounts)
	}
	if accounts[0].FollowOnApprove {
		t.Fatalf("legacy follow_on_approve must default to false")
	}
	var followedAt int64
	if err = s.db.QueryRowContext(ctx, `SELECT followed_at FROM account_artists WHERE account_id='older' AND artist_id=501`).Scan(&followedAt); err != nil || followedAt != 0 {
		t.Fatalf("legacy relation followed_at=%d err=%v", followedAt, err)
	}
	var visibility string
	if err = s.db.QueryRowContext(ctx, `SELECT visibility FROM account_artists WHERE account_id='older' AND artist_id=501`).Scan(&visibility); err != nil || visibility != "public" {
		t.Fatalf("legacy relation visibility=%q err=%v", visibility, err)
	}
	var applied int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (8,9,10,11)`).Scan(&applied); err != nil || applied != 4 {
		t.Fatalf("new migration versions applied=%d err=%v", applied, err)
	}
}

func TestAccountOptionsConfigureDeleteFallbackAndPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "accounts.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, account := range []domain.Account{
		{ID: "first", Name: "First", TokenFile: "first-token"},
		{ID: "second", Name: "Second", TokenFile: "second-token"},
		{ID: "third", Name: "Third", TokenFile: "third-token"},
	} {
		if err = s.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE accounts SET created_at=CASE id WHEN 'first' THEN 100 WHEN 'second' THEN 200 ELSE 300 END`); err != nil {
		t.Fatal(err)
	}
	seedArtist(t, s, 502)
	seedArtist(t, s, 503)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES('first',502,1),('first',503,0)`); err != nil {
		t.Fatal(err)
	}

	if err = s.ConfigureAccount(ctx, "second", true, boolPointer(false)); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureAccount(ctx, "first", false, boolPointer(false)); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureAccount(ctx, "second", true, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAccount(ctx, domain.Account{ID: "second", Name: "Second renamed", PixivID: 202, TokenFile: "rotated-token"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAccount(ctx, domain.Account{ID: "first", Name: "First renamed", PixivID: 202, TokenFile: "collision"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate Pixiv identity error=%v, want conflict", err)
	}

	accounts, err := s.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := accountMap(accounts)
	if len(accounts) != 3 || !byID["second"].IsDefault || byID["second"].FollowOnApprove || byID["second"].Name != "Second renamed" || byID["second"].TokenFile != "rotated-token" || byID["first"].IsDefault || byID["first"].FollowOnApprove {
		t.Fatalf("configured/update options not preserved: %+v", accounts)
	}
	if err = s.ConfigureAccount(ctx, "first", true, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureAccount(ctx, "first", true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO jobs(id,kind,dedupe_key,payload_json,state,available_at,created_at,updated_at,cancel_requested) VALUES
	 ('follow-pending','follow_artist','follow-pending','{"account_id":"first"}','pending',1,1,1,0),
	 ('follow-running','follow_artist','follow-running','{"account_id":"first"}','running',1,1,1,0),
	 ('follow-canceled','follow_artist','follow-canceled','{"account_id":"first"}','canceled',1,1,1,0),
	 ('other-account','follow_artist','other-account','{"account_id":"second"}','pending',1,1,1,0),
	 ('other-kind','sync_artist','other-kind','{"account_id":"first"}','pending',1,1,1,0),
	 ('follow-cancel-request','follow_artist','follow-cancel-request','{"account_id":"first"}','pending',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	accounts, err = s.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID = accountMap(accounts)
	if !byID["first"].IsDefault || byID["first"].FollowedArtists != 1 || byID["first"].PendingFollows != 2 {
		t.Fatalf("first counts/default = %+v", byID["first"])
	}
	accounts, err = s.Accounts(ctx)
	if err != nil || accountMap(accounts)["first"].PendingFollows != 2 {
		t.Fatalf("cancel-requested follow counted: accounts=%+v err=%v", accounts, err)
	}
	if byID["second"].IsDefault || byID["second"].PendingFollows != 1 {
		t.Fatalf("second counts/default = %+v", byID["second"])
	}
	if err = s.DeleteAccount(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	accounts, err = s.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID = accountMap(accounts)
	if len(accounts) != 2 || !byID["second"].IsDefault || byID["third"].IsDefault {
		t.Fatalf("default deletion fallback = %+v", accounts)
	}
	var historyRows int
	var followed int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(followed),0) FROM account_artists WHERE account_id='first' AND artist_id=502`).Scan(&historyRows, &followed); err != nil || historyRows != 1 || followed != 0 {
		t.Fatalf("deleted account source history rows=%d followed=%d err=%v", historyRows, followed, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	accounts, err = s.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID = accountMap(accounts)
	if len(accounts) != 2 || !byID["second"].IsDefault || byID["second"].FollowOnApprove || byID["third"].FollowOnApprove {
		t.Fatalf("reopened account options = %+v", accounts)
	}
	if err = s.ConfigureAccount(ctx, "", true, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty ID error=%v, want invalid", err)
	}
	if err = s.ConfigureAccount(ctx, "first", true, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted account error=%v, want not found", err)
	}
}

func boolPointer(value bool) *bool { return &value }

func accountMap(accounts []domain.Account) map[string]domain.Account {
	result := make(map[string]domain.Account, len(accounts))
	for _, account := range accounts {
		result[account.ID] = account
	}
	return result
}
