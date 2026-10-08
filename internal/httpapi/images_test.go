package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/media"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f imageRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSelectionQueryStrictValidationAndMappings(t *testing.T) {
	for _, raw := range []string{
		"?unknown=1",
		"?r18=0&r18=2",
		"?orientation=diagonal",
		"?orientation=",
		"?strategy=",
		"?orientation=%ZZ",
		"?strategy=fast",
		"?format=xml",
		"?collection=all",
		"?r18=3",
		"?ai_type=maybe",
		"?min_width=0",
		"?min_height=abc",
		"?user_id=0",
		"?user_id=9223372036854775808",
	} {
		t.Run(raw, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/random"+raw, nil)
			if _, err := selectionQuery(r); err == nil {
				t.Fatalf("selectionQuery(%q) accepted invalid input", raw)
			}
		})
	}

	parsed, err := selectionQuery(httptest.NewRequest(http.MethodGet, "/random?format=json&collection=preferred&r18=2&ai_type=any&min_width=640&min_height=480&user_id=9007199254740993&orientation=landscape&strategy=random", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.AllowedRatings, []string{"safe", "r18", "r18g"}) || !reflect.DeepEqual(parsed.AllowedAI, []string{"non_ai", "ai"}) {
		t.Fatalf("enum mapping: ratings=%v ai=%v", parsed.AllowedRatings, parsed.AllowedAI)
	}
	if parsed.MinWidth != 640 || parsed.MinHeight != 480 || parsed.ArtistID != 9007199254740993 || parsed.Orientation != "landscape" || parsed.Strategy != "random" {
		t.Fatalf("parsed selection query=%+v", parsed)
	}

	tags := url.Values{}
	tags.Add("included_tags", " Cat |DOG ")
	tags.Add("included_tags", "bird|Fox")
	tags.Add("excluded_tags", "BadTag")
	tags.Add("excluded_tags", "OTHER")
	parsed, err = selectionQuery(httptest.NewRequest(http.MethodGet, "/random?"+tags.Encode(), nil))
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := [][]string{{"cat", "dog"}, {"bird", "fox"}}
	if !reflect.DeepEqual(parsed.IncludeTags, wantGroups) {
		t.Fatalf("included tag OR groups=%v; want %v", parsed.IncludeTags, wantGroups)
	}
	if !reflect.DeepEqual(parsed.ExcludeTags, []string{"badtag", "other"}) {
		t.Fatalf("excluded tags=%v", parsed.ExcludeTags)
	}

	parsed, err = selectionQuery(httptest.NewRequest(http.MethodGet, "/random", nil))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AllowedRatings != nil || parsed.AllowedAI != nil || parsed.IncludeTags != nil || parsed.ExcludeTags != nil || parsed.MinWidth != 0 || parsed.MinHeight != 0 || parsed.ArtistID != 0 {
		t.Fatalf("omitted filters should stay unset for global preferences to apply: %+v", parsed)
	}
}

func TestImagesRoutesPreserveIdentityAndEnforceEligibility(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/images.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalog := app.NewCatalog(store)
	const artistID int64 = 9007199254740993
	const illustID int64 = 9007199254740995
	if err := catalog.ImportArtist(ctx, artistID); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DecideArtist(ctx, artistID, "approve"); err != nil {
		t.Fatal(err)
	}
	original := fmt.Sprintf("https://i.pximg.net/img-original/%d_p0.jpg", illustID)
	preview := fmt.Sprintf("https://i.pximg.net/c/600x600/img-master/%d_p0_master1200.jpg", illustID)
	if err := store.UpsertArtwork(ctx, domain.Artwork{
		ID: illustID, ArtistID: artistID, Title: "test", Kind: "illust", PageCount: 1,
		Rating: "safe", AIStatus: "non_ai", MetadataState: "complete", Visible: true,
		Pages: []domain.Page{{Index: 0, OriginalURL: original, PreviewURL: preview, Width: 1200, Height: 1800, State: "indexed"}},
	}); err != nil {
		t.Fatal(err)
	}

	var targets []string
	proxy := media.NewProxy(imageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		targets = append(targets, r.URL.String())
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		h.Add("Set-Cookie", "upstream=secret")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("pixels")), ContentLength: 6}, nil
	}))
	auth := NewAuth([]byte(adminTestToken), AuthOptions{})
	mux := http.NewServeMux()
	RegisterImages(mux, app.NewImages(store, "https://curator.test"), proxy, auth)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/i/%d/0", illustID), nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "pixels" {
			t.Fatalf("image request %d status=%d body=%q", i, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("image Cache-Control=%q", rec.Header().Get("Cache-Control"))
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("proxy leaked upstream Set-Cookie: %q", rec.Header().Get("Set-Cookie"))
		}
	}
	if !reflect.DeepEqual(targets, []string{original, original}) {
		t.Fatalf("fixed image identity targets=%v", targets)
	}

	randomRec := httptest.NewRecorder()
	mux.ServeHTTP(randomRec, httptest.NewRequest(http.MethodGet, "/random", nil))
	if randomRec.Code != http.StatusOK {
		t.Fatalf("random status=%d body=%s", randomRec.Code, randomRec.Body.String())
	}
	var selected map[string]any
	if err := json.Unmarshal(randomRec.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if selected["illust_id"] != fmt.Sprint(illustID) || selected["artist_id"] != fmt.Sprint(artistID) {
		t.Fatalf("random response IDs should be strings: %#v", selected)
	}
	if randomRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("random Cache-Control=%q", randomRec.Header().Get("Cache-Control"))
	}

	// An omitted request width leaves the global preference in force.
	globalRules := domain.DefaultRules()
	globalRules.MinWidth = 2000
	rawRules, err := json.Marshal(globalRules)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(ctx, "preferences", rawRules, 0); err != nil {
		t.Fatal(err)
	}
	globalRec := httptest.NewRecorder()
	mux.ServeHTTP(globalRec, httptest.NewRequest(http.MethodGet, "/random", nil))
	if globalRec.Code != http.StatusNotFound || !strings.Contains(globalRec.Body.String(), `"code":"NO_MATCH"`) {
		t.Fatalf("global min_width should remain active for omitted query: status=%d body=%s", globalRec.Code, globalRec.Body.String())
	}

	previewPath := fmt.Sprintf("/admin/api/previews/%d/0", illustID)
	previewRec := httptest.NewRecorder()
	mux.ServeHTTP(previewRec, httptest.NewRequest(http.MethodGet, previewPath, nil))
	if previewRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview status=%d", previewRec.Code)
	}
	previewReq := httptest.NewRequest(http.MethodGet, previewPath, nil)
	previewReq.Header.Set("Authorization", "Bearer "+adminTestToken)
	previewRec = httptest.NewRecorder()
	mux.ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("authenticated preview status=%d body=%s", previewRec.Code, previewRec.Body.String())
	}
	if targets[len(targets)-1] != preview {
		t.Fatalf("preview used target %q; want %q", targets[len(targets)-1], preview)
	}

	if _, err := catalog.DecideArtist(ctx, artistID, "block"); err != nil {
		t.Fatal(err)
	}
	before := len(targets)
	blocked := httptest.NewRecorder()
	mux.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/i/%d/0", illustID), nil))
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("blocked old image status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if len(targets) != before {
		t.Fatalf("blocked artist old URL reached upstream: targets=%v", targets)
	}
}

func TestRandomEmptyCatalogReturnsCatalogEmptyAndNoStore(t *testing.T) {
	store, err := sqlite.Open(context.Background(), t.TempDir()+"/empty.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mux := http.NewServeMux()
	RegisterImages(mux, app.NewImages(store, "https://curator.test"), media.NewProxy(imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("empty catalog should not proxy")
		return nil, nil
	})), NewAuth([]byte(adminTestToken), AuthOptions{}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/random", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty catalog status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"CATALOG_EMPTY"`) {
		t.Fatalf("empty catalog error body=%s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("empty catalog Cache-Control=%q", rec.Header().Get("Cache-Control"))
	}
}
