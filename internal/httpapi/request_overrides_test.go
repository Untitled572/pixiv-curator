package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestRandomMangaOverrideSurvivesFixedURLAndStreams(t *testing.T) {
	ctx := context.Background()
	store, mux := previewFixture(t, imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}, nil
	}))
	if _, err := store.DecideArtist(ctx, 99, "approve"); err != nil {
		t.Fatal(err)
	}
	rec := previewRequest(mux, http.MethodGet, "/random?exclude_manga=0&excluded_tags=first&excluded_tags=second", false)
	var out app.ImageResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("random status=%d body=%s", rec.Code, rec.Body)
	}
	if out.IllustID != 991 || !strings.Contains(out.URL, "exclude_manga=0") {
		t.Fatalf("selection=%+v; URL must preserve manga override", out)
	}
	path := strings.TrimPrefix(out.URL, "https://curator.test")
	stream := previewRequest(mux, http.MethodGet, path, false)
	if stream.Code != http.StatusOK || stream.Body.String() != "jpeg" {
		t.Fatalf("fixed stream status=%d body=%q", stream.Code, stream.Body.String())
	}
}

func TestRandomR18OverrideFixedURLAndHardExclusions(t *testing.T) {
	ctx := context.Background()
	store, mux := previewFixture(t, imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}, nil
	}))
	if _, err := store.DecideArtist(ctx, 99, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertArtwork(ctx, domain.Artwork{ID: 991, ArtistID: 99, Kind: "illust", PageCount: 2, Rating: "r18", AIStatus: "non_ai", MetadataState: "complete", Visible: true, Pages: []domain.Page{{Index: 0, OriginalURL: previewOriginal, PreviewURL: previewThumbnail, Width: 1200, Height: 800, State: "indexed"}, {Index: 1, OriginalURL: "https://i.pximg.net/img-original/991_p1.jpg", Width: 1200, Height: 800, State: "indexed"}}}); err != nil {
		t.Fatal(err)
	}
	rec := previewRequest(mux, http.MethodGet, "/random?r18=1", false)
	var out app.ImageResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("random status=%d body=%s", rec.Code, rec.Body)
	}
	if out.IllustID != 991 || !strings.Contains(out.URL, "r18=1") {
		t.Fatalf("selection=%+v; URL must preserve R18 override", out)
	}
	path := strings.TrimPrefix(out.URL, "https://curator.test")
	stream := previewRequest(mux, http.MethodGet, path, false)
	if stream.Code != http.StatusOK || stream.Body.String() != "jpeg" {
		t.Fatalf("fixed stream status=%d body=%q", stream.Code, stream.Body.String())
	}
	if err := store.SetArtworkPreference(ctx, 991, "dislike"); err != nil {
		t.Fatal(err)
	}
	if denied := previewRequest(mux, http.MethodGet, path, false); denied.Code != http.StatusNotFound {
		t.Fatalf("disliked fixed URL status=%d body=%s", denied.Code, denied.Body)
	}
	if err := store.SetArtworkPreference(ctx, 991, "neutral"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideArtist(ctx, 99, "block"); err != nil {
		t.Fatal(err)
	}
	if denied := previewRequest(mux, http.MethodGet, path, false); denied.Code != http.StatusNotFound {
		t.Fatalf("blocked fixed URL status=%d body=%s", denied.Code, denied.Body)
	}
}

func TestAdminArtistDetailAuthenticationAndIdentityGap(t *testing.T) {
	handler, store := newAdminTestServer(t)
	ctx := context.Background()
	if err := store.UpsertArtist(ctx, domain.Artist{ID: 950, Name: "detail"}, "manual", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideArtist(ctx, 950, "approve"); err != nil {
		t.Fatal(err)
	}
	if rec := adminRequest(handler, http.MethodGet, "/admin/api/artists/950", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated detail status=%d", rec.Code)
	}
	rec := adminRequest(handler, http.MethodGet, "/admin/api/artists/950", "", true)
	var artist map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &artist) != nil {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body)
	}
	if artist["id"] != "950" || artist["review_status"] != "approved" || artist["collection_enabled"] != true {
		t.Fatalf("artist detail state=%#v", artist)
	}
	missing := adminRequest(handler, http.MethodGet, "/admin/api/artists/951", "", true)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("missing detail status=%d body=%s", missing.Code, missing.Body)
	}
}
