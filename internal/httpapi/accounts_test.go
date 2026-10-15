package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/accounts"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type accountsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f accountsRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func accountsOAuthTransport(ids map[string]int64) http.RoundTripper {
	return accountsRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://oauth.secure.pixiv.net/auth/token" {
			return nil, fmt.Errorf("unexpected OAuth URL %q", r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		token := form.Get("refresh_token")
		id, ok := ids[token]
		if !ok {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		payload := fmt.Sprintf(`{"access_token":"access","expires_in":3600,"user":{"id":%s}}`, strconv.FormatInt(id, 10))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
}

func accountsAPI(t *testing.T) (http.Handler, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := accounts.Open(ctx, db, filepath.Join(t.TempDir(), "private"), "", pixiv.OAuthConfig{ClientID: "test-client", ClientSecret: "test-secret", HashSecret: "test-hash"}, accountsOAuthTransport(map[string]int64{"api-one": 301, "api-two": 302, "api-duplicate": 301}))
	if err != nil {
		t.Fatal(err)
	}
	auth := NewAuth([]byte(adminTestToken), AuthOptions{PublicOrigin: "https://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	RegisterAccounts(mux, m, auth)
	return mux, db
}

func accountsRequest(h http.Handler, method, path, body string, bearer bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAccountsAPIAuthenticationSecretsAndIdentityGuards(t *testing.T) {
	h, _ := accountsAPI(t)
	for _, tc := range []struct{ method, path, body string }{{http.MethodGet, "/admin/api/accounts", ""}, {http.MethodPost, "/admin/api/accounts", `{"name":"A","refresh_token":"api-one"}`}, {http.MethodPut, "/admin/api/accounts/example", `{"name":"A"}`}, {http.MethodDelete, "/admin/api/accounts/example", ""}} {
		if rec := accountsRequest(h, tc.method, tc.path, tc.body, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s status=%d", tc.method, tc.path, rec.Code)
		}
	}
	rec := accountsRequest(h, http.MethodPost, "/admin/api/accounts", `{"name":"First","refresh_token":"api-one"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		PixivID string `json:"pixiv_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.PixivID != "301" {
		t.Fatalf("created response=%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "api-one") || strings.Contains(rec.Body.String(), "refresh_token") || strings.Contains(rec.Body.String(), "token_file") {
		t.Fatalf("create response disclosed credential data: %s", rec.Body.String())
	}
	rec = accountsRequest(h, http.MethodPost, "/admin/api/accounts", `{"name":"Duplicate","refresh_token":"api-duplicate"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate Pixiv ID status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = accountsRequest(h, http.MethodPut, "/admin/api/accounts/"+created.ID, `{"name":"Changed identity","refresh_token":"api-two"}`, true)
	if rec.Code != http.StatusConflict {
		t.Fatalf("identity-changing update status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = accountsRequest(h, http.MethodGet, "/admin/api/accounts", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "api-one") || strings.Contains(rec.Body.String(), "api-two") || strings.Contains(rec.Body.String(), "token_file") {
		t.Fatalf("account list disclosed credential data: %s", rec.Body.String())
	}
	rec = accountsRequest(h, http.MethodDelete, "/admin/api/accounts/"+created.ID, "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = accountsRequest(h, http.MethodGet, "/admin/api/accounts", "", true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("deleted account remains listed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAccountsCookieWritesRequireOriginAndCSRF(t *testing.T) {
	h, _ := accountsAPI(t)
	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"`+adminTestToken+`"}`))
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var session, csrf *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		switch c.Name {
		case sessionCookieName:
			session = c
		case csrfCookieName:
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		t.Fatal("login did not issue session and CSRF cookies")
	}
	write := httptest.NewRequest(http.MethodPost, "/admin/api/accounts", strings.NewReader(`{"name":"Cookie","refresh_token":"api-one"}`))
	write.AddCookie(session)
	write.AddCookie(csrf)
	write.Header.Set("Origin", "https://curator.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, write)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF header status=%d", rec.Code)
	}
	write = httptest.NewRequest(http.MethodPost, "/admin/api/accounts", strings.NewReader(`{"name":"Cookie","refresh_token":"api-one"}`))
	write.AddCookie(session)
	write.AddCookie(csrf)
	write.Header.Set("Origin", "https://other.test")
	write.Header.Set("X-CSRF-Token", csrf.Value)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, write)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie write with bad origin status=%d", rec.Code)
	}
	write = httptest.NewRequest(http.MethodPost, "/admin/api/accounts", strings.NewReader(`{"name":"Cookie","refresh_token":"api-one"}`))
	write.AddCookie(session)
	write.AddCookie(csrf)
	write.Header.Set("Origin", "https://curator.test")
	write.Header.Set("X-CSRF-Token", csrf.Value)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, write)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid cookie write status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "api-one") {
		t.Fatalf("cookie write response disclosed token: %s", rec.Body.String())
	}
}
