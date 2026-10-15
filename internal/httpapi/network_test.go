package httpapi

import (
	"github.com/Untitled572/pixiv-curator/internal/network"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkSettingsAuthAndCredentialRedaction(t *testing.T) {
	m, _ := network.Open(filepath.Join(t.TempDir(), "private", "network.json"))
	auth := NewAuth([]byte(adminTestToken), AuthOptions{PublicOrigin: "http://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	RegisterNetwork(mux, m, auth)
	path := "/admin/api/settings/network"
	for _, method := range []string{"GET", "PUT"} {
		if r := adminRequest(mux, method, path, `{}`, false); r.Code != 401 {
			t.Fatal("unauthenticated access")
		}
	}
	r := adminRequest(mux, "PUT", path, `{"mode":"custom","proxy_url":"http://name:secret-proxy@127.0.0.1:7890"}`, true)
	if r.Code != 200 || strings.Contains(r.Body.String(), "secret-proxy") || strings.Contains(r.Body.String(), "name:") {
		t.Fatal("unsafe save response")
	}
	if r = adminRequest(mux, "GET", path, "", true); r.Code != 200 || strings.Contains(r.Body.String(), "secret-proxy") {
		t.Fatal("unsafe status")
	}
	login := adminRequest(mux, "POST", "/admin/login", `{"token":"`+adminTestToken+`"}`, false)
	req := httptest.NewRequest("PUT", path, strings.NewReader(`{"mode":"direct"}`))
	for _, cookie := range login.Result().Cookies() {
		req.AddCookie(cookie)
	}
	req.Header.Set("Origin", "http://curator.test")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("missing CSRF allowed")
	}
	if r = adminRequest(mux, "PUT", path, `{"mode":"custom","proxy_url":"file:///tmp/proxy"}`, true); r.Code != 422 {
		t.Fatal("invalid scheme accepted")
	}
}
