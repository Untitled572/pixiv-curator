package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/media"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

const previewOriginal = "https://i.pximg.net/img-original/991_p0.jpg"
const previewThumbnail = "https://i.pximg.net/img-master/991_p0_master1200.jpg"

func previewFixture(t *testing.T, transport http.RoundTripper, cacheDir ...string) (*sqlite.Store, *http.ServeMux) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/preview.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// A recommendation sample is deliberately pending and unavailable to public selection.
	if err := store.UpsertArtist(ctx, domain.Artist{ID: 99, Name: "candidate"}, "recommend", "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertArtwork(ctx, domain.Artwork{
		ID: 991, ArtistID: 99, Kind: "manga", PageCount: 2, Rating: "safe", AIStatus: "non_ai",
		MetadataState: "complete", Visible: true, Pages: []domain.Page{
			{Index: 0, OriginalURL: previewOriginal, PreviewURL: previewThumbnail, State: "indexed"},
			{Index: 1, OriginalURL: "https://i.pximg.net/img-original/991_p1.jpg", State: "indexed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	proxy := media.NewProxy(transport)
	if len(cacheDir) > 0 {
		if err := proxy.EnablePreviewCache(cacheDir[0]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(proxy.CloseIdleConnections)
	RegisterImages(mux, app.NewImages(store, "https://curator.test"), proxy, NewAuth([]byte(adminTestToken), AuthOptions{}))
	return store, mux
}

func previewRequest(mux *http.ServeMux, method, path string, authenticated bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+adminTestToken)
		req.Header.Set("Cookie", "browser-session=private")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestManagementPreviewStreamsKnownPageWithoutCredentials(t *testing.T) {
	var upstream []*http.Request
	store, mux := previewFixture(t, imageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstream = append(upstream, r.Clone(r.Context()))
		headers := make(http.Header)
		headers.Set("Content-Type", "image/jpeg")
		headers.Set("Set-Cookie", "upstream=private")
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}, nil
	}))
	publicRules := domain.DefaultRules()
	publicRules.ExcludeManga = true
	rulesJSON, err := json.Marshal(publicRules)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(context.Background(), "preferences", rulesJSON, 0); err != nil {
		t.Fatal(err)
	}
	path := "/admin/api/previews/991/0"
	unauthenticated := previewRequest(mux, "GET", path, false)
	if unauthenticated.Code != 401 || len(upstream) != 0 {
		t.Fatal("unauthenticated preview reached upstream")
	}
	for _, tc := range []struct{ method, path, target string }{
		{"GET", path, previewThumbnail},
		{"HEAD", path, previewThumbnail},
		{"GET", path + "?format=stream", previewThumbnail},
		{"GET", "/admin/api/previews/991/1", "https://i.pximg.net/img-original/991_p1.jpg"},
	} {
		rec := previewRequest(mux, tc.method, tc.path, true)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("preview status=%d type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		if tc.method == "HEAD" {
			if rec.Body.Len() != 0 {
				t.Fatal("HEAD returned image bytes")
			}
		} else if rec.Body.String() != "jpeg" {
			t.Fatalf("streamed body=%q", rec.Body.String())
		}
		if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("preview leaked headers or enabled caching")
		}
		got := upstream[len(upstream)-1]
		if got.URL.String() != tc.target || got.Method != tc.method || got.Header.Get("Referer") != "https://www.pixiv.net/" {
			t.Fatalf("unexpected preview upstream request: method=%s url=%s", got.Method, got.URL)
		}
		if got.Header.Get("Authorization") != "" || got.Header.Get("Cookie") != "" {
			t.Fatal("management credentials leaked to image CDN")
		}
	}
	calls := len(upstream)
	for _, path := range []string{"/random", "/i/991/0"} {
		if rec := previewRequest(mux, "GET", path, false); rec.Code == 200 {
			t.Fatal("pending sample leaked through public image API")
		}
	}
	for _, query := range []string{"?url=https://evil.test/", "?format=xml", "?format=stream&format=json", "?format=%ZZ", "?size=full", "?size=original&size=thumbnail", "?unknown=1&unknown=2"} {
		if rec := previewRequest(mux, "GET", "/admin/api/previews/991/0"+query, true); rec.Code != 422 {
			t.Fatalf("invalid preview query %q status=%d", query, rec.Code)
		}
	}
	if rec := previewRequest(mux, "GET", "/i/991/0?format=stream", false); rec.Code != 422 {
		t.Fatalf("public endpoint accepted stream format: %d", rec.Code)
	}
	if len(upstream) != calls {
		t.Fatal("public selection or invalid query fetched an image")
	}
}

func TestManagementPreviewFailureQueuesOneRepair(t *testing.T) {
	store, mux := previewFixture(t, imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: http.NoBody}, nil
	}))
	for i := 0; i < 2; i++ {
		rec := previewRequest(mux, "GET", "/admin/api/previews/991/0", true)
		if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"code":"MEDIA_UNAVAILABLE"`) {
			t.Fatalf("preview failure status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	jobs, err := store.Jobs(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	repairs := 0
	for _, job := range jobs {
		if job.Kind == "repair_media" {
			repairs++
		}
	}
	if repairs != 1 {
		t.Fatalf("repair jobs=%d, want one deduplicated job", repairs)
	}
}

type failedPreviewBody struct{}

func (failedPreviewBody) Read([]byte) (int, error) { return 0, errors.New("broken image stream") }
func (failedPreviewBody) Close() error             { return nil }

func TestManagementPreviewAbortsBrokenStream(t *testing.T) {
	_, mux := previewFixture(t, imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		return &http.Response{StatusCode: 200, Header: h, Body: failedPreviewBody{}, ContentLength: -1}, nil
	}))
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("broken image response was not aborted: %v", got)
		}
	}()
	previewRequest(mux, "GET", "/admin/api/previews/991/0", true)
}

func TestManagementPreviewCacheRequiresAuthAndOnlyCachesThumbnail(t *testing.T) {
	var upstream []*http.Request
	cacheDir := t.TempDir()
	store, mux := previewFixture(t, imageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstream = append(upstream, r.Clone(r.Context()))
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}, nil
	}), cacheDir)
	path := "/admin/api/previews/991/0"
	if got := previewRequest(mux, "GET", path, true); got.Code != 200 {
		t.Fatalf("cache fill status=%d", got.Code)
	}
	if got := previewRequest(mux, "GET", path, true); got.Code != 200 || got.Body.String() != "jpeg" {
		t.Fatalf("cache hit status=%d body=%q", got.Code, got.Body.String())
	}
	jsonResponse := previewRequest(mux, "GET", path+"?format=json", true)
	if jsonResponse.Code != http.StatusOK || strings.Contains(jsonResponse.Body.String(), previewOriginal) || strings.Contains(jsonResponse.Body.String(), "PreviewIsThumbnail") {
		t.Fatalf("preview JSON exposed internal or original URL fields: status=%d body=%s", jsonResponse.Code, jsonResponse.Body.String())
	}
	if len(upstream) != 1 {
		t.Fatalf("thumbnail upstream requests=%d, want cache hit", len(upstream))
	}
	if got := previewRequest(mux, "HEAD", path, true); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d", got.Code)
	}
	if len(upstream) != 1 {
		t.Fatal("HEAD did not use cached thumbnail headers")
	}
	originalHead := previewRequest(mux, "HEAD", path+"?size=original", true)
	if originalHead.Code != 200 || originalHead.Body.Len() != 0 || upstream[len(upstream)-1].URL.String() != previewOriginal {
		t.Fatalf("original HEAD status=%d body=%q upstream=%v", originalHead.Code, originalHead.Body.String(), upstream[len(upstream)-1].URL)
	}
	if upstream[len(upstream)-1].Header.Get("Referer") != "https://www.pixiv.net/" || upstream[len(upstream)-1].Header.Get("Authorization") != "" || upstream[len(upstream)-1].Header.Get("Cookie") != "" {
		t.Fatal("original HEAD request forwarded management credentials or omitted Pixiv Referer")
	}
	for i := 0; i < 2; i++ {
		got := previewRequest(mux, "GET", path+"?size=original", true)
		if got.Code != 200 || got.Body.String() != "jpeg" {
			t.Fatalf("original stream status=%d body=%q", got.Code, got.Body.String())
		}
		if upstream[len(upstream)-1].URL.String() != previewOriginal {
			t.Fatalf("size=original fetched %q, want fixed original page %q", upstream[len(upstream)-1].URL, previewOriginal)
		}
	}
	if len(upstream) != 4 {
		t.Fatalf("original stream requests=%d, want all requests to bypass thumbnail cache", len(upstream))
	}
	originalJSON := previewRequest(mux, "GET", path+"?format=json&size=original", true)
	var originalPayload map[string]any
	if originalJSON.Code != 200 || json.Unmarshal(originalJSON.Body.Bytes(), &originalPayload) != nil || originalPayload["url"] != previewOriginal {
		t.Fatalf("original JSON lost size selection: status=%d body=%s", originalJSON.Code, originalJSON.Body)
	}
	originalRedirect := previewRequest(mux, "GET", path+"?format=redirect&size=original", true)
	if originalRedirect.Code != http.StatusFound || originalRedirect.Header().Get("Location") != previewOriginal {
		t.Fatalf("original redirect lost size selection: status=%d location=%q", originalRedirect.Code, originalRedirect.Header().Get("Location"))
	}
	for i := 0; i < 2; i++ {
		if got := previewRequest(mux, "GET", "/admin/api/previews/991/1", true); got.Code != 200 {
			t.Fatalf("fallback status=%d", got.Code)
		}
	}
	if len(upstream) != 6 {
		t.Fatalf("original URLs were cached: upstream requests=%d urls=%v", len(upstream), upstream)
	}
	files, err := filepath.Glob(filepath.Join(cacheDir, "*.cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("cached files=%d; want thumbnail only", len(files))
	}
	if got := previewRequest(mux, "GET", path+"?size=original", false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated original status=%d", got.Code)
	}
	if err := os.WriteFile(files[0], []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(upstream)
	if got := previewRequest(mux, "GET", path, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", got.Code)
	}
	if len(upstream) != before {
		t.Fatal("unauthenticated request reached upstream")
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("unauthenticated request accessed cache: %v", err)
	}
	if _, err := store.DecideArtist(context.Background(), 99, "block"); err != nil {
		t.Fatal(err)
	}
	before = len(upstream)
	for _, query := range []string{"", "?size=original"} {
		if got := previewRequest(mux, "GET", path+query, true); got.Code != http.StatusNotFound {
			t.Fatalf("blocked preview %q status=%d body=%s", query, got.Code, got.Body)
		}
	}
	if len(upstream) != before {
		t.Fatal("blocked thumbnail or original preview reached upstream")
	}
}
