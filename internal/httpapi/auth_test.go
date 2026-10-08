package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthBearerAndFailClosed(t *testing.T) {
	protected := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		name   string
		auth   *Auth
		header string
		want   int
	}{
		{name: "missing configured token", auth: NewAuth(nil, AuthOptions{}), header: "Bearer secret", want: http.StatusUnauthorized},
		{name: "wrong token", auth: NewAuth([]byte("secret"), AuthOptions{}), header: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "valid token", auth: NewAuth([]byte("secret"), AuthOptions{}), header: "Bearer secret", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/api/artists", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			tc.auth.Middleware(protected).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d; want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestSessionLoginCSRFAndLogout(t *testing.T) {
	auth := NewAuth([]byte("test-secret"), AuthOptions{SecureCookies: true, SessionTTL: time.Hour, PublicOrigin: "https://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	protected := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/admin/api/", auth.Middleware(protected))

	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"test-secret"}`))
	loginRec := httptest.NewRecorder()
	mux.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d; body=%s", loginRec.Code, loginRec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(loginRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "authenticated" {
		t.Fatalf("unexpected login response: %#v", body)
	}
	if strings.Contains(loginRec.Body.String(), "test-secret") {
		t.Fatal("login response disclosed credential")
	}
	cookies := loginRec.Result().Cookies()
	var session, csrf *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			session = c
		}
		if c.Name == csrfCookieName {
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		t.Fatalf("missing session or CSRF cookie: %#v", cookies)
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie flags are unsafe: %#v", session)
	}
	if csrf.HttpOnly || !csrf.Secure || csrf.SameSite != http.SameSiteStrictMode {
		t.Fatalf("CSRF cookie flags are unsafe: %#v", csrf)
	}

	get := httptest.NewRequest(http.MethodGet, "/admin/api/artists", nil)
	get.AddCookie(session)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusNoContent {
		t.Fatalf("session GET status = %d", getRec.Code)
	}

	write := httptest.NewRequest(http.MethodPost, "/admin/api/artists", nil)
	write.AddCookie(session)
	write.AddCookie(csrf)
	write.Header.Set("Origin", "https://curator.test")
	writeRec := httptest.NewRecorder()
	mux.ServeHTTP(writeRec, write)
	if writeRec.Code != http.StatusForbidden {
		t.Fatalf("unprotected cookie write status = %d; want 403", writeRec.Code)
	}
	write.Header.Set("X-CSRF-Token", csrf.Value)
	writeRec = httptest.NewRecorder()
	mux.ServeHTTP(writeRec, write)
	if writeRec.Code != http.StatusNoContent {
		t.Fatalf("CSRF-protected write status = %d", writeRec.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	logout.AddCookie(session)
	logout.AddCookie(csrf)
	logout.Header.Set("Origin", "https://curator.test")
	logout.Header.Set("X-CSRF-Token", csrf.Value)
	logoutRec := httptest.NewRecorder()
	mux.ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout status = %d", logoutRec.Code)
	}
	getRec = httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d; want 401", getRec.Code)
	}
}

func TestLoginRateLimitAndBadInput(t *testing.T) {
	auth := NewAuth([]byte("test-secret"), AuthOptions{})
	for i := 0; i < loginMaxFailures; i++ {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"wrong"}`))
		rec := httptest.NewRecorder()
		auth.login(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d", i, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"test-secret"}`))
	rec := httptest.NewRecorder()
	auth.login(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit status = %d; want 429", rec.Code)
	}
}
