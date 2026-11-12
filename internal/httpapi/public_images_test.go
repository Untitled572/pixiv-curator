package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/app"
)

func TestPublicImageURLsStreamOriginalWithoutConsumerReferer(t *testing.T) {
	var upstream []*http.Request
	cacheDir := t.TempDir()
	store, mux := previewFixture(t, imageRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstream = append(upstream, r.Clone(r.Context()))
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		h.Set("Set-Cookie", "upstream=private")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("jpeg")), ContentLength: 4}, nil
	}), cacheDir)
	if _, err := store.DecideArtist(context.Background(), 99, "approve"); err != nil {
		t.Fatal(err)
	}
	rec := previewRequest(mux, "GET", "/random", false)
	var selected app.ImageResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &selected) != nil {
		t.Fatalf("JSON status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(selected.URL, "https://curator.test/i/991/") || !strings.HasSuffix(selected.URL, "?format=image") || !strings.HasPrefix(selected.OriginalURL, "https://i.pximg.net/img-original/") {
		t.Fatalf("delivery URLs=%+v", selected)
	}
	if len(upstream) != 0 {
		t.Fatal("JSON fetched image bytes")
	}
	path := strings.TrimPrefix(selected.URL, "https://curator.test")
	for _, method := range []string{"GET", "HEAD"} {
		got := previewRequest(mux, method, path, true)
		if got.Code != 200 || got.Header().Get("Content-Type") != "image/jpeg" || got.Header().Get("Set-Cookie") != "" || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("image headers status=%d headers=%v", got.Code, got.Header())
		}
		if method == "GET" && got.Body.String() != "jpeg" {
			t.Fatal("fixed image did not return original bytes")
		}
		if method == "HEAD" && got.Body.Len() != 0 {
			t.Fatal("HEAD returned bytes")
		}
		request := upstream[len(upstream)-1]
		if request.Header.Get("Referer") != "https://www.pixiv.net/" || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.URL.String() != selected.OriginalURL {
			t.Fatalf("unsafe/mismatched upstream request: %s", request.URL)
		}
	}
	if got := previewRequest(mux, "GET", "/random?format=image", false); got.Code != 200 || got.Body.String() != "jpeg" {
		t.Fatalf("random image status=%d", got.Code)
	}
	calls := len(upstream)
	for _, path := range []string{"/random?delivery=original", "/i/991/0?delivery=original"} {
		got := previewRequest(mux, "GET", path, false)
		var result app.ImageResult
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &result) != nil || result.URL != result.OriginalURL || !strings.HasPrefix(result.URL, "https://i.pximg.net/") {
			t.Fatalf("origin compatibility status=%d body=%s", got.Code, got.Body)
		}
	}
	if got := previewRequest(mux, "GET", "/random?format=redirect", false); got.Code != 302 || !strings.HasPrefix(got.Header().Get("Location"), "https://curator.test/i/991/") {
		t.Fatal("redirect bypassed local image endpoint")
	}
	if got := previewRequest(mux, "GET", "/i/991/0?format=redirect&delivery=original", false); got.Code != 302 || got.Header().Get("Location") != previewOriginal {
		t.Fatal("explicit original redirect missing")
	}
	for _, path := range []string{"/random?format=image&delivery=original", "/random?delivery=invalid", "/i/991/0?format=image&url=https://evil.test", "/i/991/0?format=image&delivery=original", "/admin/api/previews/991/0?delivery=original"} {
		if got := previewRequest(mux, "GET", path, true); got.Code != 422 {
			t.Errorf("%s status=%d want422", path, got.Code)
		}
	}
	if len(upstream) != calls {
		t.Fatal("metadata/redirect/invalid request fetched an image")
	}
	files, err := filepath.Glob(filepath.Join(cacheDir, "*.cache"))
	if err != nil || len(files) != 0 {
		t.Fatalf("public original persisted in preview cache: %v %v", files, err)
	}
	if _, err = store.DecideArtist(context.Background(), 99, "block"); err != nil {
		t.Fatal(err)
	}
	if got := previewRequest(mux, "GET", path, false); got.Code != 404 {
		t.Fatalf("blocked fixed image status=%d", got.Code)
	}
	if len(upstream) != calls {
		t.Fatal("blocked image reached upstream")
	}
}

func TestPublicImageFailureQueuesRepairWithoutReturningRedirect(t *testing.T) {
	store, mux := previewFixture(t, imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("forbidden"))}, nil
	}))
	if _, err := store.DecideArtist(context.Background(), 99, "approve"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got := previewRequest(mux, "GET", "/i/991/0?format=image", false)
		if got.Code != 502 || !strings.Contains(got.Body.String(), "MEDIA_UNAVAILABLE") || got.Header().Get("Location") != "" {
			t.Fatalf("bad gateway response: %d %s", got.Code, got.Body)
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
		t.Fatalf("repair tasks=%d want1", repairs)
	}
}
