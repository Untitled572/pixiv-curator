package accounts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

func TestManagerFollowingRestrictedRoutesVisibilityAndBindsIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy-token")
	if err := os.WriteFile(legacyPath, []byte("legacy-token"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	transport := accountRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth.secure.pixiv.net" {
			return fakeOAuth(map[string]int64{"legacy-token": 101}).RoundTrip(r)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/user/following" || r.URL.Query().Get("user_id") != "101" || r.URL.Query().Get("restrict") != "private" {
			return nil, fmt.Errorf("unexpected restricted-follow request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"user_previews":[]}`))}, nil
	})
	m, err := Open(ctx, db, filepath.Join(dir, "private"), legacyPath, pixiv.OAuthConfig{ClientID: "test", ClientSecret: "test"}, transport)
	if err != nil {
		t.Fatal(err)
	}
	page, err := m.FollowingRestricted(domain.WithFollowVisibility(domain.WithAccount(ctx, "legacy"), "private"), "", "private")
	if err != nil || len(page.Artists) != 0 {
		t.Fatalf("restricted page=%+v err=%v", page, err)
	}
	accounts, err := m.List(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].PixivID != 101 {
		t.Fatalf("account identity binding=%+v err=%v", accounts, err)
	}
}

func TestManagerFollowingRestrictedRejectsWrongAccountRouting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, filepath.Join(dir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := Open(ctx, db, filepath.Join(dir, "private"), "", pixiv.OAuthConfig{ClientID: "test", ClientSecret: "test"}, fakeOAuth(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.FollowingRestricted(domain.WithAccount(ctx, "missing"), "", "private")
	apiErr, ok := err.(*pixiv.Error)
	if !ok || apiErr.Code != "ACCOUNT_REMOVED" {
		t.Fatalf("missing-account routing error=%v", err)
	}
}
