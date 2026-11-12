package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

func newLibraryHandler(t *testing.T) (*http.ServeMux, *sqlite.Store) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), t.TempDir()+"/library.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mux := http.NewServeMux()
	RegisterLibrary(mux, s, NewAuth([]byte(adminTestToken), AuthOptions{}))
	return mux, s
}

func libraryRequest(h http.Handler, method, path, body string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLibraryTagRoutesAuthenticateAndRestoreOverride(t *testing.T) {
	h, s := newLibraryHandler(t)
	ctx := context.Background()
	if err := s.UpsertArtist(ctx, domain.Artist{ID: 81, Name: "artist"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.NewCatalog(s).DecideArtist(ctx, 81, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertArtwork(ctx, domain.Artwork{ID: 82, ArtistID: 81, MetadataState: "complete", Tags: []string{"automatic"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{{http.MethodGet, "/admin/api/library/artists", ""}, {http.MethodGet, "/admin/api/library/tags", ""}, {http.MethodPut, "/admin/api/library/artists/81/tags", `{"tags":[]}`}, {http.MethodDelete, "/admin/api/library/artists/81/tags", ""}} {
		if rec := libraryRequest(h, tc.method, tc.path, tc.body, false); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s status=%d", tc.method, tc.path, rec.Code)
		}
	}
	rec := libraryRequest(h, http.MethodPut, "/admin/api/library/artists/81/tags", `{"tags":[]}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty override status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = libraryRequest(h, http.MethodGet, "/admin/api/library/artists?limit=1", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tags":[]`) {
		t.Fatalf("empty override list status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = libraryRequest(h, http.MethodDelete, "/admin/api/library/artists/81/tags", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = libraryRequest(h, http.MethodGet, "/admin/api/library/artists?tag=automatic", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"81"`) {
		t.Fatalf("automatic tag filter status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/library/artists?limit=1&limit=2", "", true); rec.Code != 422 {
		t.Fatalf("duplicate query status=%d", rec.Code)
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/library/artists?page=1&after=81", "", true); rec.Code != 422 {
		t.Fatalf("mixed pagination status=%d", rec.Code)
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/library/artists?page=1", "", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"library_total":1`) || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("page response status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/stats", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stats status=%d", rec.Code)
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/stats?extra=1", "", true); rec.Code != 422 {
		t.Fatalf("stats extra parameter status=%d", rec.Code)
	}
	if rec = libraryRequest(h, http.MethodGet, "/admin/api/stats", "", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"unknown_rating_artworks":1`) {
		t.Fatalf("stats status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLibraryStatsRejectMalformedQuery(t *testing.T) {
	h, _ := newLibraryHandler(t)
	for _, path := range []string{"/admin/api/stats?%ZZ", "/admin/api/library/artists?page=1&tag=%ZZ", "/admin/api/library/tags?%ZZ"} {
		if rec := libraryRequest(h, http.MethodGet, path, "", true); rec.Code != 422 {
			t.Errorf("%s status=%d, want 422", path, rec.Code)
		}
	}
}
