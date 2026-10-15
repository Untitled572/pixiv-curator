package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagesRenderEmbeddedHTMLWithoutCredentialValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		marker  string
	}{
		{name: "login", handler: Login, marker: `id="login-form"`},
		{name: "home", handler: Home, marker: `data-tab="library"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tc.handler(recorder, httptest.NewRequest(http.MethodGet, "/admin/", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d", recorder.Code)
			}
			if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type=%q", got)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, tc.marker) {
				t.Fatalf("rendered page missing marker %q", tc.marker)
			}
			if strings.Contains(body, "never-render-this-token") {
				t.Fatal("rendered page included credential data")
			}
			if strings.Contains(body, "localStorage") || strings.Contains(body, ".innerHTML") {
				t.Fatal("page includes unsafe credential storage or HTML injection API")
			}
			if tc.name == "home" {
				for _, marker := range []string{`data-tab="discover"`, `data-tab="settings"`, `id="artist-list"`, `id="account-create-form"`, `id="preferences-form"`} {
					if !strings.Contains(body, marker) {
						t.Errorf("management page missing %q", marker)
					}
				}
				for _, removed := range []string{`data-tab="jobs"`, `id="recommendation-run-id"`, `id="pixiv-login-start"`, `id="approve-selected"`} {
					if strings.Contains(body, removed) {
						t.Errorf("management page still includes retired UI %q", removed)
					}
				}
			}
		})
	}
}

func TestPageHandlersAllowOnlyGET(t *testing.T) {
	for _, handler := range []http.HandlerFunc{Login, Home} {
		recorder := httptest.NewRecorder()
		handler(recorder, httptest.NewRequest(http.MethodPost, "/admin/", nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST status=%d", recorder.Code)
		}
	}
}
