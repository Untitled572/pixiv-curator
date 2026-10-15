package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type accountRoundTripFunc func(*http.Request) (*http.Response, error)

func (f accountRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeOAuth(ids map[string]int64) http.RoundTripper {
	return accountRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://oauth.secure.pixiv.net/auth/token" {
			return nil, fmt.Errorf("unexpected OAuth URL %q", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		refresh := form.Get("refresh_token")
		id, ok := ids[refresh]
		if !ok {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		payload := fmt.Sprintf(`{"access_token":"access-%s","expires_in":3600,"user":{"id":%s}}`, refresh, strconv.FormatInt(id, 10))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
}

func TestManagerAccountIsolationPersistenceAndDeletion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	transport := fakeOAuth(map[string]int64{"token-a": 101, "token-b": 202, "token-duplicate": 101})
	oauth := pixiv.OAuthConfig{ClientID: "test-client", ClientSecret: "test-secret", HashSecret: "test-hash"}
	m, err := Open(ctx, db, filepath.Join(dir, "private"), "", oauth, transport)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Save(ctx, "", "First", "token-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Save(ctx, "", "Second", "token-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.PixivID != 101 || b.PixivID != 202 {
		t.Fatalf("accounts are not isolated: a=%+v b=%+v", a, b)
	}
	if _, err = m.Save(ctx, "", "Duplicate", "token-duplicate"); err != domain.ErrConflict {
		t.Fatalf("duplicate Pixiv ID error=%v; want conflict", err)
	}
	originalToken, err := os.ReadFile(a.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Save(ctx, a.ID, "First updated", "token-b"); err != domain.ErrConflict {
		t.Fatalf("identity-changing update error=%v; want conflict", err)
	}
	still, err := os.ReadFile(a.TokenFile)
	if err != nil || string(still) != string(originalToken) {
		t.Fatalf("failed identity update changed credential: %q, %v", still, err)
	}
	accounts, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(accounts)
	for _, secret := range []string{"token-a", "token-b", "token-duplicate", a.TokenFile, b.TokenFile} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("account listing leaked credential %q: %s", secret, encoded)
		}
	}
	if st, err := os.Stat(a.TokenFile); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("credential mode=%v err=%v", st, err)
	}

	// A new manager reconstructs account-specific clients from persistent rows.
	restarted, err := Open(ctx, db, filepath.Join(dir, "private"), "", oauth, transport)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := restarted.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Status != "configured" || listed[1].Status != "configured" {
		t.Fatalf("accounts after restart=%+v", listed)
	}
	if err = restarted.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(a.TokenFile); !os.IsNotExist(err) {
		t.Fatalf("deleted account credential still exists: %v", err)
	}
	if _, err = restarted.Following(domain.WithAccount(ctx, a.ID), ""); err == nil {
		t.Fatal("deleted account remained routable")
	} else if apiErr, ok := err.(*pixiv.Error); !ok || apiErr.Code != "ACCOUNT_REMOVED" {
		t.Fatalf("deleted account routing error=%v", err)
	}
	listed, err = restarted.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != b.ID {
		t.Fatalf("deleted account remains in list: %+v", listed)
	}
}

func TestLegacyIdentityVerifiedBeforeAddingAccounts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy-token")
	if err := os.WriteFile(legacy, []byte("old-token"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oauthTransport := fakeOAuth(map[string]int64{"old-token": 101, "duplicate": 101, "other": 202})
	transport := accountRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "app-api.pixiv.net" {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"user_previews":[],"next_url":""}`))}, nil
		}
		return oauthTransport.RoundTrip(r)
	})
	oauth := pixiv.OAuthConfig{ClientID: "test", ClientSecret: "test"}
	m, err := Open(ctx, db, filepath.Join(dir, "private"), legacy, oauth, transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Save(ctx, "", "Other", "other"); err != domain.ErrConflict {
		t.Fatalf("unknown legacy allowed duplicate registration: %v", err)
	}
	if _, err = m.Following(domain.WithAccount(ctx, "legacy"), ""); err != nil {
		t.Fatal(err)
	}
	list, err := m.List(ctx)
	if err != nil || len(list) != 1 || list[0].PixivID != 101 {
		t.Fatalf("legacy identity not persisted: %+v %v", list, err)
	}
	if _, err = m.Save(ctx, "", "Duplicate", "duplicate"); err != domain.ErrConflict {
		t.Fatalf("legacy duplicate accepted: %v", err)
	}
	added, err := m.Save(ctx, "", "Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process interruption between metadata deletion and private file cleanup.
	if err = db.DeleteAccount(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(ctx, db, filepath.Join(dir, "private"), legacy, oauth, transport); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(added.TokenFile); !os.IsNotExist(err) {
		t.Fatal("deleted managed credential was not recovered")
	}
}
