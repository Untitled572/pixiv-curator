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

func (s *cleanupTestStore) LibraryCleanupReview(context.Context) ([]domain.LibraryCleanupItem, error) {
	return []domain.LibraryCleanupItem{{Artist: domain.Artist{ID: 7, ReviewStatus: "approved"}, SampleIllustIDs: []string{"70"}}}, nil
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
