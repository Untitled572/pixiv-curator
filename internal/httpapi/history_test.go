package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

func historyRequest(mux *http.ServeMux, path string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHistoryRouteRequiresAuthAndReturnsFixedPagedRecords(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/history.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	RegisterHistory(mux, store, NewAuth([]byte(adminTestToken), AuthOptions{}))

	if got := historyRequest(mux, "/admin/api/history?page=1&limit=2", false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history status=%d", got.Code)
	}
	empty := historyRequest(mux, "/admin/api/history", true)
	var emptyPage domain.DistributionHistoryPage
	if empty.Code != http.StatusOK || json.Unmarshal(empty.Body.Bytes(), &emptyPage) != nil || emptyPage.Items == nil || len(emptyPage.Items) != 0 || emptyPage.TotalPages != 0 || emptyPage.Page != 1 || emptyPage.PageSize != 24 {
		t.Fatalf("empty history status=%d body=%s page=%+v", empty.Code, empty.Body, emptyPage)
	}

	if err := store.UpsertArtist(ctx, domain.Artist{ID: 420, Name: "history artist"}, "manual", "history-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideArtist(ctx, 420, "approve"); err != nil {
		t.Fatal(err)
	}
	const illustID int64 = 4201
	if err := store.UpsertArtwork(ctx, domain.Artwork{
		ID: illustID, ArtistID: 420, Title: "history title", Kind: "illust", PageCount: 1,
		Rating: "safe", AIStatus: "non_ai", MetadataState: "complete",
		Pages: []domain.Page{{Index: 0, OriginalURL: "https://i.pximg.net/img-original/4201_p0.jpg", Width: 1200, Height: 1800, State: "indexed"}},
	}); err != nil {
		t.Fatal(err)
	}
	request := domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"}, Orientation: "any"}}
	for range 3 {
		if _, err := store.Select(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DecideArtist(ctx, 420, "block"); err != nil {
		t.Fatal(err)
	}

	first := historyRequest(mux, "/admin/api/history?page=1&limit=2", true)
	var firstPage domain.DistributionHistoryPage
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil {
		t.Fatalf("history status=%d body=%s", first.Code, first.Body)
	}
	if firstPage.Total != 3 || firstPage.Page != 1 || firstPage.PageSize != 2 || firstPage.TotalPages != 2 || len(firstPage.Items) != 2 {
		t.Fatalf("first page metadata=%+v", firstPage)
	}
	seen := map[string]bool{}
	for _, item := range firstPage.Items {
		seen[item.ID] = true
		if item.IllustID != illustID || item.PageIndex != 0 || item.ArtistID != 420 || item.ArtistName != "history artist" || item.Title != "history title" || item.Width != 1200 || item.Height != 1800 {
			t.Fatalf("history metadata=%+v", item)
		}
		if item.Available || item.ThumbnailURL != fmt.Sprintf("/admin/api/previews/%d/0", illustID) || item.FullURL != fmt.Sprintf("/admin/api/previews/%d/0?size=original", illustID) {
			t.Fatalf("blocked item availability or preview URLs=%+v", item)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("duplicate events collapsed: %+v", firstPage.Items)
	}
	second := historyRequest(mux, "/admin/api/history?page=2&limit=2", true)
	var secondPage domain.DistributionHistoryPage
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || len(secondPage.Items) != 1 || secondPage.Total != 3 {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body)
	}
	for _, secret := range []string{"https://i.pximg.net", "refresh_token", "original_url"} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatalf("history leaked raw URL or credential field %q", secret)
		}
	}
	for _, bad := range []string{
		"?unknown=1", "?page=0", "?page=-1", "?page=+1", "?page=1&page=2", "?limit=0", "?limit=-1", "?limit=97", "?limit=2&limit=3", "?page=abc", "?page=1&unknown=x", "?page=%ZZ",
	} {
		if got := historyRequest(mux, "/admin/api/history"+bad, true); got.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid query %q status=%d body=%s", bad, got.Code, got.Body)
		}
	}
}
