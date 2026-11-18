package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type cleanupTestStore struct{ removed int64 }

func (s *cleanupTestStore) LibraryCleanupReview(context.Context) (int64, []domain.LibraryCleanupItem, []string, error) {
	return 1, []domain.LibraryCleanupItem{{Artist: domain.Artist{ID: 7, ReviewStatus: "approved"}, SampleIllustIDs: []string{"70"}}}, []string{}, nil
}
func (s *cleanupTestStore) NextLibraryCleanup(ctx context.Context, round int64) (int64, []domain.LibraryCleanupItem, []string, error) {
	return round + 1, []domain.LibraryCleanupItem{}, []string{}, nil
}
func (s *cleanupTestStore) KeepLibraryCleanupArtist(ctx context.Context, id, round int64) error {
	return nil
}
func (s *cleanupTestStore) RemoveLibraryArtist(_ context.Context, id int64) error {
	s.removed = id
	return nil
}

func TestLibraryCleanupRoutesRequireAuthAndRejectQuery(t *testing.T) {
	store := &cleanupTestStore{}
	auth := NewAuth([]byte(adminTestToken), AuthOptions{})
	mux := http.NewServeMux()
	RegisterLibraryCleanup(mux, store, auth)
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/admin/api/library/review", "", http.StatusUnauthorized},
		{"GET", "/admin/api/library/review", "Bearer " + adminTestToken, http.StatusOK},
		{"GET", "/admin/api/library/review?limit=99", "Bearer " + adminTestToken, http.StatusUnprocessableEntity},
		{"POST", "/admin/api/library/artists/7/remove?force=1", "Bearer " + adminTestToken, http.StatusUnprocessableEntity},
		{"POST", "/admin/api/library/artists/7/remove", "Bearer " + adminTestToken, http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
		if tc.token != "" {
			req.Header.Set("Authorization", tc.token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s status=%d want=%d body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if store.removed != 7 {
		t.Fatalf("removed id=%d", store.removed)
	}
}

func TestCleanupNewRoutesAuthenticateAndDecode(t *testing.T) {
	store := &cleanupTestStore{}
	mux := http.NewServeMux()
	RegisterLibraryCleanup(mux, store, NewAuth([]byte(adminTestToken), AuthOptions{}))
	for _, tc := range []struct {
		path, body, auth string
		want             int
	}{
		{"/admin/api/library/review/next", `{"round":1}`, "", http.StatusUnauthorized},
		{"/admin/api/library/review/next", `{"round":1}`, "Bearer " + adminTestToken, http.StatusOK},
		{"/admin/api/library/artists/7/keep", `{"round":1}`, "Bearer " + adminTestToken, http.StatusOK},
		{"/admin/api/library/artists/no/keep", `{"round":1}`, "Bearer " + adminTestToken, http.StatusUnprocessableEntity},
		{"/admin/api/library/review/next?x=1", `{"round":1}`, "Bearer " + adminTestToken, http.StatusUnprocessableEntity},
		{"/admin/api/library/review/next", `not-json`, "Bearer " + adminTestToken, http.StatusUnprocessableEntity},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", tc.auth)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: got %d want %d body=%s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}
