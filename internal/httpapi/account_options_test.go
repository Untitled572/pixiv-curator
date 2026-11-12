package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/accounts"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

func TestAccountOptionsHTTPContractAndAuthentication(t *testing.T) {
	ctx := context.Background()
	h, store, manager, source, target := newAccountOptionsAPI(t)
	if err := primeCopyFollowing(t, store, source.ID, 9701); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{{http.MethodPatch, "/admin/api/accounts/" + target.ID, `{"is_default":true}`}, {http.MethodPost, "/admin/api/accounts/" + target.ID + "/copy-following", `{"source_account_id":"` + source.ID + `"}`}} {
		if rec := accountOptionsRequest(h, tc.method, tc.path, tc.body, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s status=%d", tc.method, tc.path, rec.Code)
		}
	}

	before := countFollowJobs(t, store)
	if rec := accountOptionsRequest(h, http.MethodGet, "/admin/api/accounts", "", true); rec.Code != http.StatusOK {
		t.Fatalf("GET account metadata status=%d body=%s", rec.Code, rec.Body.String())
	} else {
		for _, secret := range []string{"api-one", "api-two", "token_file", "/private/", `private\`} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("account metadata leaked %q: %s", secret, rec.Body.String())
			}
		}
	}
	for _, path := range []string{"/admin/api/accounts/" + target.ID, "/admin/api/accounts/" + target.ID + "/copy-following"} {
		if rec := accountOptionsRequest(h, http.MethodGet, path, "", true); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET mutation route %s status=%d, want 405", path, rec.Code)
		}
	}
	if after := countFollowJobs(t, store); after != before {
		t.Fatalf("GET requests wrote follow jobs: before=%d after=%d", before, after)
	}

	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"`+adminTestToken+`"}`))
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("cookie login status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var session, csrf *http.Cookie
	for _, cookie := range loginRec.Result().Cookies() {
		switch cookie.Name {
		case sessionCookieName:
			session = cookie
		case csrfCookieName:
			csrf = cookie
		}
	}
	if session == nil || csrf == nil {
		t.Fatal("login did not issue session and CSRF cookies")
	}
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{{http.MethodPatch, "/admin/api/accounts/" + target.ID, `{"follow_on_approve":false}`}, {http.MethodPost, "/admin/api/accounts/" + target.ID + "/copy-following", `{"source_account_id":"` + source.ID + `"}`}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.AddCookie(session)
		req.AddCookie(csrf)
		req.Header.Set("Origin", "https://curator.test")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cookie write missing CSRF %s %s status=%d", tc.method, tc.path, rec.Code)
		}
	}

	patchPath := "/admin/api/accounts/" + target.ID
	for _, tc := range []struct {
		path string
		body string
	}{{patchPath, `{}`}, {patchPath, `{"unknown":true}`}, {patchPath + "?unexpected=1", `{"is_default":true}`}, {patchPath, `{"is_default":false}`}, {patchPath, `{"follow_on_approve":"false"}`}, {patchPath, `{"follow_on_approve":true} {}`}} {
		if rec := accountOptionsRequest(h, http.MethodPatch, tc.path, tc.body, true); rec.Code != 422 {
			t.Errorf("invalid PATCH %s %s status=%d body=%s", tc.path, tc.body, rec.Code, rec.Body.String())
		}
	}
	copyPath := "/admin/api/accounts/" + target.ID + "/copy-following"
	for _, tc := range []struct {
		path string
		body string
	}{{copyPath, `{}`}, {copyPath, `{"source_account_id":"` + source.ID + `","unknown":1}`}, {copyPath + "?extra=x", `{"source_account_id":"` + source.ID + `"}`}, {copyPath, `{"source_account_id":""}`}} {
		if rec := accountOptionsRequest(h, http.MethodPost, tc.path, tc.body, true); rec.Code != 422 {
			t.Errorf("invalid copy POST %s %s status=%d body=%s", tc.path, tc.body, rec.Code, rec.Body.String())
		}
	}
	if rec := accountOptionsRequest(h, http.MethodPost, copyPath, `{"source_account_id":"`+target.ID+`"}`, true); rec.Code != 422 {
		t.Fatalf("copy source=target status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := accountOptionsRequest(h, http.MethodPatch, patchPath, `{"is_default":true,"follow_on_approve":false}`, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "api-two") || strings.Contains(rec.Body.String(), "token_file") {
		t.Fatalf("valid PATCH status=%d body=%s", rec.Code, rec.Body.String())
	}
	accounts, err := manager.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != target.ID || !accounts[0].IsDefault || accounts[0].FollowOnApprove {
		t.Fatalf("PATCH options not persisted: %+v", accounts)
	}
	rec = accountOptionsRequest(h, http.MethodPost, copyPath, `{"source_account_id":"`+source.ID+`"}`, true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"queued":1`) || strings.Contains(rec.Body.String(), "api-one") {
		t.Fatalf("valid copy POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	if after := countFollowJobs(t, store); after != before+1 {
		t.Fatalf("copy POST queued unexpected count: before=%d after=%d", before, after)
	}
}

func TestAccountOptionsRejectDuplicateFields(t *testing.T) {
	h, _, _, source, target := newAccountOptionsAPI(t)
	for _, body := range []string{`{"follow_on_approve":false,"follow_on_approve":true}`, `{"follow_on_approve":false,"Follow_On_Approve":true}`, `null`, `[]`} {
		rec := accountOptionsRequest(h, http.MethodPatch, "/admin/api/accounts/"+target.ID, body, true)
		if rec.Code != 422 {
			t.Fatalf("duplicate/non-object body accepted: status=%d", rec.Code)
		}
	}
	body := `{"source_account_id":"` + source.ID + `","source_account_id":"` + target.ID + `"}`
	if rec := accountOptionsRequest(h, http.MethodPost, "/admin/api/accounts/"+target.ID+"/copy-following", body, true); rec.Code != 422 {
		t.Fatalf("duplicate copy field accepted: %d", rec.Code)
	}
}

func newAccountOptionsAPI(t *testing.T) (http.Handler, *sqlite.Store, *accounts.Manager, domain.Account, domain.Account) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "account-options.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := accounts.Open(ctx, store, filepath.Join(dir, "private"), "", pixiv.OAuthConfig{ClientID: "test-client", ClientSecret: "test-secret", HashSecret: "test-hash"}, accountsOAuthTransport(map[string]int64{"api-one": 601, "api-two": 602}))
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Save(ctx, "", "Source", "api-one")
	if err != nil {
		t.Fatal(err)
	}
	target, err := manager.Save(ctx, "", "Target", "api-two")
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuth([]byte(adminTestToken), AuthOptions{PublicOrigin: "https://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	RegisterAccounts(mux, manager, auth)
	RegisterAccountOptions(mux, manager, auth)
	return mux, store, manager, source, target
}

func accountOptionsRequest(h http.Handler, method, path, body string, bearer bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func primeCopyFollowing(t *testing.T, store *sqlite.Store, sourceID string, artistID int64) error {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertArtist(ctx, domain.Artist{ID: artistID, Name: "source-follow"}, "manual", "options-test"); err != nil {
		return err
	}
	var job domain.Job
	for attempts := 0; attempts < 20; attempts++ {
		candidate, err := store.Claim(ctx, time.Now().Add(time.Second))
		if err != nil {
			return err
		}
		if candidate.Key == "following:"+sourceID {
			job = candidate
			break
		}
		if err = store.Finish(ctx, candidate, "succeeded", "", time.Now()); err != nil {
			return err
		}
	}
	if job.ID == "" {
		return domain.ErrNotFound
	}
	payload, _ := json.Marshal(map[string]string{"account_id": sourceID, "cursor": "", "restrict": "private"})
	return store.CommitFollowingBatch(domain.WithFollowVisibility(domain.WithAccount(ctx, sourceID), "private"), job, "source-run", []domain.Artist{{ID: artistID, Name: "source-follow"}}, payload, true)
}

func countFollowJobs(t *testing.T, store *sqlite.Store) int {
	t.Helper()
	jobs, err := store.Jobs(context.Background(), "", 200)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, job := range jobs {
		if job.Kind == "follow_artist" {
			count++
		}
	}
	return count
}
