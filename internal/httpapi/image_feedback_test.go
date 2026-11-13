package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type imageFeedbackTestStore struct {
	id    int64
	page  int
	value string
}

func (s *imageFeedbackTestStore) SetImageFeedback(_ context.Context, id int64, page int, value string) (string, error) {
	s.id = id
	s.page = page
	s.value = value
	return value, nil
}

func TestImageFeedbackAuthCSRFAndStrictInput(t *testing.T) {
	store := &imageFeedbackTestStore{}
	auth := NewAuth([]byte(adminTestToken), AuthOptions{PublicOrigin: "https://curator.test"})
	mux := http.NewServeMux()
	auth.Register(mux)
	RegisterImageFeedback(mux, store, auth)
	call := func(path, body string, cookies []*http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if got := call("/admin/api/image-feedback", `{"illust_id":"9","page_index":0,"value":"like"}`, nil, "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", got.Code)
	}
	login := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(`{"token":"`+adminTestToken+`"}`))
	loginRec := httptest.NewRecorder()
	mux.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var cookies []*http.Cookie
	var csrf string
	for _, c := range loginRec.Result().Cookies() {
		cookies = append(cookies, c)
		if c.Name == csrfCookieName {
			csrf = c.Value
		}
	}
	body := `{"illust_id":"9","page_index":0,"value":"like"}`
	if got := call("/admin/api/image-feedback", body, cookies, "", "https://curator.test"); got.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", got.Code)
	}
	if got := call("/admin/api/image-feedback", body, cookies, csrf, "https://curator.test"); got.Code != http.StatusOK || got.Body.String() != `{"value":"like"}`+"\n" {
		t.Fatalf("valid feedback status=%d body=%s", got.Code, got.Body.String())
	}
	for _, tc := range []struct{ path, body string }{{"/admin/api/image-feedback?x=1", body}, {"/admin/api/image-feedback", `{"illust_id":"9","page_index":0,"value":"like","extra":1}`}, {"/admin/api/image-feedback", `{"illust_id":9,"page_index":0,"value":"like"}`}, {"/admin/api/image-feedback", `{"illust_id":"9","page_index":-1,"value":"like"}`}, {"/admin/api/image-feedback", `{"illust_id":"9","page_index":0,"value":"bad"}`}, {"/admin/api/image-feedback", body + ` {}`}} {
		if got := call(tc.path, tc.body, cookies, csrf, "https://curator.test"); got.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid %s %s status=%d body=%s", tc.path, tc.body, got.Code, got.Body.String())
		}
	}
	if store.id != 9 || store.page != 0 || store.value != "like" {
		t.Fatalf("store input=%+v", store)
	}
	if got := call("/admin/api/image-feedback", body, cookies, csrf, "https://evil.test"); got.Code != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d", got.Code)
	}
}
