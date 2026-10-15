package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countingWriter struct {
	header http.Header
	status int
	bytes  int64
}

func newCountingWriter() *countingWriter         { return &countingWriter{header: make(http.Header)} }
func (w *countingWriter) Header() http.Header    { return w.header }
func (w *countingWriter) WriteHeader(status int) { w.status = status }
func (w *countingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.bytes += int64(len(p))
	return len(p), nil
}

type readCloseFunc struct {
	read  func([]byte) (int, error)
	close func() error
}

func (r *readCloseFunc) Read(p []byte) (int, error) { return r.read(p) }
func (r *readCloseFunc) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func imageResponse(status int, body io.ReadCloser, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: body, ContentLength: -1}
}
func successfulImage(body io.ReadCloser) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", "image/jpeg")
	return imageResponse(http.StatusOK, body, h)
}
func request(method string) *http.Request {
	return httptest.NewRequest(method, "https://curator.test/media", nil)
}
func requireMediaError(t *testing.T, err error, code string, started bool) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("error=%v; want *media.Error", err)
	}
	if got.Code != code || got.Started != started {
		t.Fatalf("error=%+v; want code=%q started=%v", got, code, started)
	}
}

func TestForwardStripsCredentialsAndRangeSetsRefererAndHidesSetCookie(t *testing.T) {
	var got *http.Request
	body := &readCloseFunc{read: func(p []byte) (int, error) { return copy(p, "image-bytes"), io.EOF }}
	proxy := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.Clone(r.Context())
		h := make(http.Header)
		h.Set("Content-Type", "image/jpeg")
		h.Add("Set-Cookie", "session=secret")
		return imageResponse(http.StatusOK, body, h), nil
	}))
	incoming := request(http.MethodGet)
	incoming.Header.Set("Authorization", "Bearer account-secret")
	incoming.Header.Set("Cookie", "session=account-secret")
	incoming.Header.Set("Range", "bytes=0-99")
	w := httptest.NewRecorder()
	if err := proxy.Forward(w, incoming, "https://i.pximg.net/img-original/1.jpg"); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("transport was not called")
	}
	for _, key := range []string{"Authorization", "Cookie", "Range"} {
		if value := got.Header.Get(key); value != "" {
			t.Errorf("upstream %s=%q; credential/range must not be forwarded", key, value)
		}
	}
	if got.Header.Get("Referer") != "https://www.pixiv.net/" {
		t.Errorf("Referer=%q", got.Header.Get("Referer"))
	}
	if got.Header.Get("User-Agent") != "PixivCurator/1" || got.Header.Get("Accept") != "image/*" {
		t.Errorf("unexpected proxy headers: %#v", got.Header)
	}
	if w.Code != http.StatusOK || w.Body.String() != "image-bytes" {
		t.Fatalf("response status=%d body=%q", w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("upstream Set-Cookie leaked: %q", w.Header().Get("Set-Cookie"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", w.Header().Get("Cache-Control"))
	}
}

func TestForwardHEADDoesNotReadUpstreamBody(t *testing.T) {
	var reads atomic.Int32
	body := &readCloseFunc{read: func([]byte) (int, error) { reads.Add(1); return 0, errors.New("body must not be read") }}
	proxy := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) { return successfulImage(body), nil }))
	w := httptest.NewRecorder()
	if err := proxy.Forward(w, request(http.MethodHead), "https://i.pximg.net/img-original/2.jpg"); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 0 {
		t.Fatalf("HEAD read upstream body %d times", reads.Load())
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("HEAD response status=%d bytes=%d", w.Code, w.Body.Len())
	}
}

func TestForwardRejectsNonImageAndMapsUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            int
		contentType, code string
	}{
		{"non-image", 200, "text/html", "INVALID_MEDIA"},
		{"forbidden", 403, "image/jpeg", "MEDIA_UNAVAILABLE"},
		{"missing", 404, "image/jpeg", "MEDIA_UNAVAILABLE"},
		{"server-error", 502, "image/jpeg", "UPSTREAM_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			h.Set("Content-Type", tc.contentType)
			proxy := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return imageResponse(tc.status, io.NopCloser(strings.NewReader("body")), h), nil
			}))
			w := httptest.NewRecorder()
			err := proxy.Forward(w, request(http.MethodGet), "https://i.pximg.net/img-original/3.jpg")
			requireMediaError(t, err, tc.code, false)
			if w.Body.Len() != 0 {
				t.Fatalf("body was written before validation: %d bytes", w.Body.Len())
			}
		})
	}
}

func TestForwardReportsTimeoutAndContextCancellation(t *testing.T) {
	proxy := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	proxy.http.Timeout = 10 * time.Millisecond
	w := httptest.NewRecorder()
	err := proxy.Forward(w, request(http.MethodGet), "https://i.pximg.net/img-original/4.jpg")
	requireMediaError(t, err, "UPSTREAM_UNAVAILABLE", false)

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	proxy = NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	result := make(chan error, 1)
	go func() {
		result <- proxy.Forward(httptest.NewRecorder(), request(http.MethodGet).WithContext(ctx), "https://i.pximg.net/img-original/5.jpg")
	}()
	<-started
	cancel()
	requireMediaError(t, <-result, "UPSTREAM_UNAVAILABLE", false)
}

func TestForwardEnforcesChunkedResponseLimit(t *testing.T) {
	remaining := MaxImageBytes + 1
	body := &readCloseFunc{read: func(p []byte) (int, error) {
		if remaining == 0 {
			return 0, io.EOF
		}
		n := int64(len(p))
		if n > remaining {
			n = remaining
		}
		remaining -= n
		for i := int64(0); i < n; i++ {
			p[i] = 'x'
		}
		return int(n), nil
	}}
	proxy := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) { return successfulImage(body), nil }))
	w := newCountingWriter()
	err := proxy.Forward(w, request(http.MethodGet), "https://i.pximg.net/img-original/chunked.jpg")
	requireMediaError(t, err, "MEDIA_TOO_LARGE", true)
	if w.bytes != MaxImageBytes {
		t.Fatalf("forwarded bytes=%d; want bounded read of %d", w.bytes, MaxImageBytes)
	}
}

func TestForwardReportsStreamFailureAfterResponseStarted(t *testing.T) {
	readErr := errors.New("broken stream")
	reads := 0
	body := &readCloseFunc{read: func(p []byte) (int, error) {
		reads++
		if reads == 1 {
			return copy(p, "partial-image"), nil
		}
		return 0, readErr
	}}
	proxy := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) { return successfulImage(body), nil }))
	w := httptest.NewRecorder()
	err := proxy.Forward(w, request(http.MethodGet), "https://i.pximg.net/img-original/broken.jpg")
	requireMediaError(t, err, "STREAM_FAILED", true)
	if w.Code != http.StatusOK || w.Body.String() != "partial-image" {
		t.Fatalf("partial response status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestForwardRejectsUnsafeTargets(t *testing.T) {
	proxy := NewProxy(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsafe target reached transport")
		return nil, nil
	}))
	for _, target := range []string{
		"http://i.pximg.net/img.jpg",
		"https://user:pass@i.pximg.net/img.jpg",
		"https://example.com/img.jpg",
		"https://i.pximg.net:444/img.jpg",
		"https://i.pximg.net:80/img.jpg",
	} {
		t.Run(target, func(t *testing.T) {
			err := proxy.Forward(httptest.NewRecorder(), request(http.MethodGet), target)
			requireMediaError(t, err, "INVALID_TARGET", false)
		})
	}
	_, err := safeDial(context.Background(), "tcp", net.JoinHostPort("localhost.invalid", "443"))
	requireMediaError(t, err, "INVALID_TARGET", false)
}

func TestForwardValidatesRedirectDestination(t *testing.T) {
	calls := 0
	proxy := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			h := make(http.Header)
			h.Set("Location", "https://attacker.example/redirect")
			return imageResponse(http.StatusFound, http.NoBody, h), nil
		}
		return successfulImage(http.NoBody), nil
	}))
	w := httptest.NewRecorder()
	err := proxy.Forward(w, request(http.MethodGet), "https://i.pximg.net/img-original/redirect.jpg")
	// net/http returns CheckRedirect errors from Client.Do; Forward currently
	// normalizes that to UPSTREAM_UNAVAILABLE while still blocking the target.
	requireMediaError(t, err, "INVALID_TARGET", false)
	if calls != 1 {
		t.Fatalf("invalid redirect target reached transport; calls=%d", calls)
	}
}

func TestForwardAllowsOnlyBoundedSameHostRedirects(t *testing.T) {
	calls := 0
	proxy := NewProxy(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls <= 3 {
			h := make(http.Header)
			h.Set("Location", fmt.Sprintf("https://i.pximg.net/redirect-%d", calls))
			return imageResponse(http.StatusFound, http.NoBody, h), nil
		}
		return successfulImage(http.NoBody), nil
	}))
	if err := proxy.Forward(httptest.NewRecorder(), request(http.MethodGet), "https://i.pximg.net/img-original/start.jpg"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("same-host redirect calls=%d; want 4", calls)
	}
}

func TestEnvironmentTransportSelectionAndTargetBoundary(t *testing.T) {
	for _, useProxy := range []bool{false, true} {
		directCalls, proxyCalls := 0, 0
		transport := environmentTransport{
			direct: roundTripFunc(func(*http.Request) (*http.Response, error) {
				directCalls++
				return successfulImage(io.NopCloser(strings.NewReader("image"))), nil
			}),
			proxied: roundTripFunc(func(*http.Request) (*http.Response, error) {
				proxyCalls++
				return successfulImage(io.NopCloser(strings.NewReader("image"))), nil
			}),
			proxy: func(*http.Request) (*url.URL, error) {
				if useProxy {
					return url.Parse("http://127.0.0.1:7890")
				}
				return nil, nil
			},
		}
		req, _ := http.NewRequest("GET", "https://i.pximg.net/img-original/4.jpg", nil)
		response, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if useProxy && (proxyCalls != 1 || directCalls != 0) || !useProxy && (directCalls != 1 || proxyCalls != 0) {
			t.Fatal("incorrect proxy routing")
		}
		req, _ = http.NewRequest("GET", "https://untrusted.example/image.jpg", nil)
		if _, err = transport.RoundTrip(req); err == nil {
			t.Fatal("invalid target accepted")
		}
		if directCalls+proxyCalls != 1 {
			t.Fatal("invalid target reached network")
		}
	}
}

func TestConfiguredProxyReceivesOnlyValidatedCDNConnect(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	p := NewProxyWithResolver(func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	err := p.Forward(httptest.NewRecorder(), request(http.MethodGet), "https://i.pximg.net/img-original/4.jpg")
	if err == nil {
		t.Fatal("failed CONNECT reported success")
	}
	select {
	case got := <-seen:
		if got != "CONNECT i.pximg.net:443" {
			t.Fatalf("unexpected proxy target: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("configured proxy was bypassed")
	}
}
