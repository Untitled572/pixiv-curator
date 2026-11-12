package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

func TestRecommendationRatingQueryValidationAuthAndLegacyRequests(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "recommendation-http.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.NewRecommender(store, nil)
	runID, _, err := service.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterRecommendations(mux, service, NewAuth([]byte("test-token"), AuthOptions{}))
	request := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer test-token")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if got := request("/admin/api/recommendation-runs/latest", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated latest status=%d", got)
	}
	if got := request("/admin/api/recommendations?run_id=missing", false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated candidates status=%d", got)
	}
	for _, path := range []string{
		"/admin/api/recommendation-runs/latest?content_rating=nope",
		"/admin/api/recommendation-runs/latest?content_rating=all&content_rating=safe",
		"/admin/api/recommendation-runs/latest?extra=1",
		"/admin/api/recommendation-runs/latest?ai_type=bad",
		"/admin/api/recommendation-runs/latest?ai_type=ai&ai_type=unknown",
		"/admin/api/recommendations?run_id=x&run_id=y",
		"/admin/api/recommendations?run_id=x&content_rating=nope",
		"/admin/api/recommendations?run_id=x&extra=1",
		"/admin/api/recommendations?run_id=x&ai_type=bad",
		"/admin/api/recommendations?run_id=x&content_rating=adult&ai_type=unknown&ai_type=ai",
		"/admin/api/recommendations?run_id=x&content_rating=%ZZ",
	} {
		if got := request(path, true).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("%s status=%d want 422", path, got)
		}
	}
	if got := request("/admin/api/recommendation-runs/latest", true).Code; got != http.StatusOK {
		t.Fatalf("legacy latest status=%d", got)
	}
	if got := request("/admin/api/recommendations?run_id="+runID, true).Code; got != http.StatusOK {
		t.Fatalf("legacy candidate status=%d", got)
	}
	if got := request("/admin/api/recommendation-runs/latest?content_rating=all", true).Code; got != http.StatusOK {
		t.Fatalf("filtered latest status=%d", got)
	}
	if got := request("/admin/api/recommendation-runs/latest?content_rating=adult&ai_type=unknown", true).Code; got != http.StatusOK {
		t.Fatalf("combined filtered latest status=%d", got)
	}
	if got := request("/admin/api/recommendations?run_id="+runID+"&content_rating=unknown&ai_type=unknown", true).Code; got != http.StatusOK {
		t.Fatalf("combined filtered candidates status=%d", got)
	}
}
