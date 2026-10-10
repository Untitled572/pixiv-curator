package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/app"
)

type accountLoginProviderFake struct {
	started chan struct{}
}

func (p accountLoginProviderFake) Enabled() bool { return true }
func (p accountLoginProviderFake) Login(ctx context.Context) (string, error) {
	close(p.started)
	<-ctx.Done()
	return "", ctx.Err()
}

type accountCredentialWriterFake struct{}

func (accountCredentialWriterFake) Credentials(context.Context, string) error { return nil }

func accountLoginHandler(t *testing.T) http.Handler {
	t.Helper()
	service := app.NewAccountLogin(context.Background(), accountLoginProviderFake{started: make(chan struct{})}, accountCredentialWriterFake{})
	t.Cleanup(service.Close)
	auth := NewAuth([]byte(adminTestToken), AuthOptions{PublicOrigin: "https://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	RegisterAccountLogin(mux, service, auth)
	return mux
}

func TestAccountLoginRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	handler := accountLoginHandler(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/api/account/login"},
		{http.MethodPost, "/admin/api/account/login"},
		{http.MethodPost, "/admin/api/account/login/cancel"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated status=%d; want 401", tc.method, tc.path, rec.Code)
		}
	}

	// Establish a browser session using the existing validated-token login.
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"`+adminTestToken+`"}`))
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("session login status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range loginRec.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("session cookie missing")
	}
	write := httptest.NewRequest(http.MethodPost, "/admin/api/account/login/cancel", strings.NewReader(`{}`))
	write.AddCookie(session)
	write.Header.Set("Origin", "https://curator.test")
	writeRec := httptest.NewRecorder()
	handler.ServeHTTP(writeRec, write)
	if writeRec.Code != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF status=%d; want 403", writeRec.Code)
	}
}

func TestAccountLoginBearerStartRejectsUnknownJSONAndReturnsStatusOnly(t *testing.T) {
	started := make(chan struct{})
	service := app.NewAccountLogin(context.Background(), accountLoginProviderFake{started: started}, accountCredentialWriterFake{})
	t.Cleanup(service.Close)
	auth := NewAuth([]byte(adminTestToken), AuthOptions{})
	mux := http.NewServeMux()
	RegisterAccountLogin(mux, service, auth)

	bad := httptest.NewRequest(http.MethodPost, "/admin/api/account/login", strings.NewReader(`{"unexpected":true}`))
	bad.Header.Set("Authorization", "Bearer "+adminTestToken)
	badRec := httptest.NewRecorder()
	mux.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown JSON status=%d; want 422; body=%s", badRec.Code, badRec.Body.String())
	}

	start := httptest.NewRequest(http.MethodPost, "/admin/api/account/login", strings.NewReader(`{}`))
	start.Header.Set("Authorization", "Bearer "+adminTestToken)
	startRec := httptest.NewRecorder()
	mux.ServeHTTP(startRec, start)
	if startRec.Code != http.StatusAccepted {
		t.Fatalf("start status=%d; want 202; body=%s", startRec.Code, startRec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider was not started")
	}
	if strings.Contains(startRec.Body.String(), "refresh") || strings.Contains(startRec.Body.String(), "token") {
		t.Fatalf("start response disclosed a credential: %s", startRec.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/admin/api/account/login", nil)
	get.Header.Set("Authorization", "Bearer "+adminTestToken)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("status GET=%d; want 200", getRec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "running" || body["enabled"] != true {
		t.Fatalf("unexpected status response: %#v", body)
	}
	if strings.Contains(getRec.Body.String(), "token") {
		t.Fatalf("status response disclosed token: %s", getRec.Body.String())
	}
}
