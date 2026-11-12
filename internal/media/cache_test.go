package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewCacheHitSurvivesRestartAndIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".preview-abandoned.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	cache, err := NewPreviewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".preview-abandoned.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan temporary file remains: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	cache.maxBytes = 80
	if err := cache.put(previewCacheKey("first"), "image/jpeg", []byte("123456")); err != nil {
		t.Fatal(err)
	}
	if err := cache.put(previewCacheKey("second"), "image/png", []byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.get(previewCacheKey("first")); ok {
		t.Fatal("oldest entry was not evicted to enforce capacity")
	}
	if _, ok := cache.get(previewCacheKey("second")); !ok {
		t.Fatal("newest entry missing")
	}

	restarted, err := NewPreviewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := restarted.get(previewCacheKey("second")); !ok || string(got.body) != "abcdef" || got.contentType != "image/png" {
		t.Fatalf("restart cache hit=(%+v,%v)", got, ok)
	}
}

func TestPreviewCacheExpiresAfterSevenDaysEvenWhenUsed(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewPreviewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	cache.now = func() time.Time { return base }
	key := previewCacheKey("expired")
	if err := cache.put(key, "image/webp", []byte("body")); err != nil {
		t.Fatal(err)
	}
	entry := cache.entries[key]
	cache.now = func() time.Time { return base.Add(6 * 24 * time.Hour) }
	if _, ok := cache.get(key); !ok {
		t.Fatal("entry expired before seven days")
	}
	cache.now = func() time.Time { return base.Add(8 * 24 * time.Hour) }
	if _, ok := cache.get(key); ok {
		t.Fatal("hot entry exceeded absolute seven-day TTL")
	}
	if _, err := os.Stat(entry.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired file remains: %v", err)
	}
}
func TestForwardPreviewCachesOnlyCompleteSmallImagesWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	var upstream *http.Request
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		upstream = r.Clone(r.Context())
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		h.Set("Set-Cookie", "private=1")
		body := []byte("jpeg-data")
		h.Set("Content-Length", "9")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body))}, nil
	})
	p := NewProxy(transport)
	if err := p.EnablePreviewCache(dir); err != nil {
		t.Fatal(err)
	}
	incoming := request(http.MethodGet)
	incoming.Header.Set("Authorization", "Bearer secret")
	incoming.Header.Set("Cookie", "private=1")
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		if err := p.ForwardPreview(w, incoming, "https://i.pximg.net/img-master/1.jpg"); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || w.Body.String() != "jpeg-data" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response %d: status=%d body=%q headers=%v", i, w.Code, w.Body.String(), w.Header())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want one cache fill", calls.Load())
	}
	if upstream == nil || upstream.Header.Get("Authorization") != "" || upstream.Header.Get("Cookie") != "" || upstream.Header.Get("Referer") != "https://www.pixiv.net/" {
		t.Fatalf("unexpected upstream headers: %v", upstream)
	}
}

func TestForwardPreviewDoesNotCacheTruncatedOrCanceledResponses(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		dir := t.TempDir()
		var calls atomic.Int32
		p := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			h := make(http.Header)
			h.Set("Content-Type", "image/jpeg")
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("cut")), ContentLength: 10}, nil
		}))
		if err := p.EnablePreviewCache(dir); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			err := p.ForwardPreview(httptest.NewRecorder(), request(http.MethodGet), "https://i.pximg.net/img-master/truncated.jpg")
			if err == nil {
				t.Fatal("truncated response accepted")
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("truncated response cached: calls=%d", calls.Load())
		}
		files, err := filepath.Glob(filepath.Join(dir, "*.cache"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 0 {
			t.Fatalf("truncated cache files=%v", files)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		dir := t.TempDir()
		started := make(chan struct{})
		var startOnce sync.Once
		p := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			h := make(http.Header)
			h.Set("Content-Type", "image/jpeg")
			body := &readCloseFunc{read: func([]byte) (int, error) {
				startOnce.Do(func() { close(started) })
				<-r.Context().Done()
				return 0, r.Context().Err()
			}}
			return &http.Response{StatusCode: 200, Header: h, Body: body, ContentLength: 4}, nil
		}))
		if err := p.EnablePreviewCache(dir); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- p.ForwardPreview(httptest.NewRecorder(), request(http.MethodGet).WithContext(ctx), "https://i.pximg.net/img-master/canceled.jpg")
		}()
		<-started
		cancel()
		if err := <-done; err == nil {
			t.Fatal("canceled request succeeded")
		}
		files, err := filepath.Glob(filepath.Join(dir, "*.cache"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 0 {
			t.Fatalf("canceled cache files=%v", files)
		}
	})
}

func TestForwardPreviewCoalescesConcurrentMisses(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	p := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return successfulImage(io.NopCloser(strings.NewReader("jpeg"))), nil
	}))
	if err := p.EnablePreviewCache(dir); err != nil {
		t.Fatal(err)
	}
	type result struct {
		body string
		err  error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			w := httptest.NewRecorder()
			err := p.ForwardPreview(w, request(http.MethodGet), "https://i.pximg.net/img-master/shared.jpg")
			results <- result{body: w.Body.String(), err: err}
		}()
		if i == 0 {
			<-started
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		got := <-results
		if got.err != nil || got.body != "jpeg" {
			t.Fatalf("coalesced result=%+v", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same-key upstream calls=%d; want one", calls.Load())
	}
}

func TestForwardPreviewAllowsFourMissesAndCancelsQueuedMiss(t *testing.T) {
	dir := t.TempDir()
	var active, maximum, calls atomic.Int32
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	p := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		current := active.Add(1)
		for previous := maximum.Load(); current > previous && !maximum.CompareAndSwap(previous, current); previous = maximum.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			active.Add(-1)
			return successfulImage(io.NopCloser(strings.NewReader("jpeg"))), nil
		case <-r.Context().Done():
			active.Add(-1)
			return nil, r.Context().Err()
		}
	}))
	if err := p.EnablePreviewCache(dir); err != nil {
		t.Fatal(err)
	}
	type result struct{ err error }
	results := make(chan result, 4)
	used := make(map[byte]bool)
	var targets []string
	for i := 0; len(targets) < 5; i++ {
		target := fmt.Sprintf("https://i.pximg.net/img-master/parallel-%d.jpg", i)
		hash := sha256.Sum256([]byte(target))
		stripe := hash[0] % 16
		if !used[stripe] {
			used[stripe] = true
			targets = append(targets, target)
		}
	}
	for i := 0; i < 4; i++ {
		target := targets[i]
		go func() {
			err := p.ForwardPreview(httptest.NewRecorder(), request(http.MethodGet), target)
			results <- result{err: err}
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("four independent misses did not reach upstream concurrently")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := p.ForwardPreview(httptest.NewRecorder(), request(http.MethodGet).WithContext(ctx), targets[4]); err == nil {
		t.Fatal("miss queued behind four upstream slots did not cancel")
	}
	if calls.Load() != 4 || maximum.Load() != 4 {
		t.Fatalf("upstream calls=%d max concurrency=%d; want exactly four active misses", calls.Load(), maximum.Load())
	}
	close(release)
	for i := 0; i < 4; i++ {
		if got := <-results; got.err != nil {
			t.Fatalf("active miss failed: %v", got.err)
		}
	}
}
