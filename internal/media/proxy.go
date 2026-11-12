// Package media forwards catalog images and manages bounded preview caching.
package media

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxImageBytes int64 = 64 << 20

type Error struct {
	Code    string
	Started bool
}

func (e *Error) Error() string { return "media: " + e.Code }

type Proxy struct {
	http         *http.Client
	previewCache *PreviewCache
	streamSlots  chan struct{}
	previewMiss  chan struct{}
	previewLocks [16]chan struct{}
}

func (p *Proxy) CloseIdleConnections() { p.http.CloseIdleConnections() }

func NewProxy(transport http.RoundTripper) *Proxy {
	return newProxy(transport, http.ProxyFromEnvironment)
}
func NewProxyWithResolver(resolve func(*http.Request) (*url.URL, error)) *Proxy {
	return newProxy(nil, resolve)
}
func newProxy(transport http.RoundTripper, resolve func(*http.Request) (*url.URL, error)) *Proxy {
	if transport == nil {
		direct := http.DefaultTransport.(*http.Transport).Clone()
		direct.Proxy = nil
		direct.ResponseHeaderTimeout = 10 * time.Second
		direct.TLSHandshakeTimeout = 10 * time.Second
		direct.DialContext = safeDial
		proxied := http.DefaultTransport.(*http.Transport).Clone()
		proxied.Proxy = func(r *http.Request) (*url.URL, error) {
			u, _ := r.Context().Value(proxyContextKey{}).(*url.URL)
			return u, nil
		}
		proxied.ResponseHeaderTimeout = 10 * time.Second
		proxied.TLSHandshakeTimeout = 10 * time.Second
		transport = environmentTransport{direct: direct, proxied: proxied, proxy: resolve}
	}
	p := &Proxy{streamSlots: make(chan struct{}, 4), previewMiss: make(chan struct{}, 4), http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !domain.ValidImageURL(r.URL.String()) {
			return &Error{Code: "INVALID_TARGET"}
		}
		return nil
	}}}
	for i := range p.previewLocks {
		p.previewLocks[i] = make(chan struct{}, 1)
	}
	return p
}

// Retain DNS pinning for direct requests, but allow the operator-configured
// proxy endpoint (often loopback) to perform CONNECT to the validated CDN URL.
type proxyContextKey struct{}

type environmentTransport struct {
	direct, proxied http.RoundTripper
	proxy           func(*http.Request) (*url.URL, error)
}

func (t environmentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !domain.ValidImageURL(r.URL.String()) {
		return nil, &Error{Code: "INVALID_TARGET"}
	}
	proxy, err := t.proxy(r)
	if err != nil {
		return nil, &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	if proxy == nil {
		return t.direct.RoundTrip(r)
	}
	return t.proxied.RoundTrip(r.Clone(context.WithValue(r.Context(), proxyContextKey{}, proxy)))
}

func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !strings.EqualFold(host, "i.pximg.net") || port != "443" {
		return nil, &Error{Code: "INVALID_TARGET"}
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, &Error{Code: "DNS_ERROR"}
	}
	for _, ip := range ips {
		if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() || !ip.IP.IsGlobalUnicast() {
			return nil, &Error{Code: "INVALID_TARGET"}
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	// Pin the resolved public address, so connection setup does not resolve again.
	var last error
	for _, ip := range ips {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = errors.New("no usable address")
	}
	return nil, last
}

// Forward receives only a trusted catalog URL. Range is intentionally ignored;
// both GET and HEAD preserve the fixed page identity supplied by the caller.
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request, target string) error {
	if !domain.ValidImageURL(target) {
		return &Error{Code: "INVALID_TARGET"}
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		return &Error{Code: "INVALID_METHOD"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	select {
	case p.streamSlots <- struct{}{}:
		defer func() { <-p.streamSlots }()
	case <-ctx.Done():
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, nil)
	if err != nil {
		return &Error{Code: "INVALID_TARGET"}
	}
	req.Header.Set("Referer", "https://www.pixiv.net/")
	req.Header.Set("User-Agent", "PixivCurator/1")
	req.Header.Set("Accept", "image/*")
	resp, err := p.http.Do(req)
	if err != nil {
		var rejected *Error
		if errors.As(err, &rejected) {
			return rejected
		}
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		code := "UPSTREAM_ERROR"
		if resp.StatusCode == 403 || resp.StatusCode == 404 {
			code = "MEDIA_UNAVAILABLE"
		}
		return &Error{Code: code}
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return &Error{Code: "INVALID_MEDIA"}
	}
	if resp.ContentLength > MaxImageBytes {
		return &Error{Code: "MEDIA_TOO_LARGE"}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	if length := resp.Header.Get("Content-Length"); length != "" {
		w.Header().Set("Content-Length", length)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == "HEAD" {
		return nil
	}
	n, err := io.CopyBuffer(w, io.LimitReader(resp.Body, MaxImageBytes), make([]byte, 32<<10))
	if err != nil {
		return &Error{Code: "STREAM_FAILED", Started: true}
	}
	if n == MaxImageBytes {
		var extra [1]byte
		read, readErr := io.ReadFull(resp.Body, extra[:])
		if read > 0 {
			return &Error{Code: "MEDIA_TOO_LARGE", Started: true}
		}
		if readErr != nil && readErr != io.EOF {
			return &Error{Code: "STREAM_FAILED", Started: true}
		}
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return &Error{Code: "STREAM_FAILED", Started: true}
	}
	return nil
}

// EnablePreviewCache attaches a private, bounded cache used only by management previews.
func (p *Proxy) EnablePreviewCache(dir string) error {
	cache, err := NewPreviewCache(dir)
	if err != nil {
		return err
	}
	p.previewCache = cache
	return nil
}

// ForwardPreview serves a cached thumbnail or buffers a complete small image
// before atomically caching it. Original-image fallback must continue using Forward.
func (p *Proxy) ForwardPreview(w http.ResponseWriter, r *http.Request, target string) error {
	if !domain.ValidImageURL(target) {
		return &Error{Code: "INVALID_TARGET"}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return &Error{Code: "INVALID_METHOD"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	key := previewCacheKey(target)
	if p.previewCache != nil {
		if r.Method == http.MethodHead {
			if contentType, size, ok := p.previewCache.head(key); ok {
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
				return nil
			}
		} else if value, ok := p.previewCache.get(key); ok {
			w.Header().Set("Content-Type", value.contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(value.body)))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(value.body); err != nil {
				return &Error{Code: "STREAM_FAILED", Started: true}
			}
			return nil
		}
	}
	if r.Method == http.MethodHead || p.previewCache == nil {
		return p.Forward(w, r, target)
	}
	stripeHash := sha256.Sum256([]byte(target))
	stripe := p.previewLocks[int(stripeHash[0])%len(p.previewLocks)]
	select {
	case stripe <- struct{}{}:
		defer func() { <-stripe }()
	case <-r.Context().Done():
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	if value, ok := p.previewCache.get(key); ok {
		w.Header().Set("Content-Type", value.contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(value.body)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(value.body); err != nil {
			return &Error{Code: "STREAM_FAILED", Started: true}
		}
		return nil
	}
	select {
	case p.previewMiss <- struct{}{}:
		defer func() { <-p.previewMiss }()
	case <-r.Context().Done():
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return &Error{Code: "INVALID_TARGET"}
	}
	req.Header.Set("Referer", "https://www.pixiv.net/")
	req.Header.Set("User-Agent", "PixivCurator/1")
	req.Header.Set("Accept", "image/*")
	resp, err := p.http.Do(req)
	if err != nil {
		var rejected *Error
		if errors.As(err, &rejected) {
			return rejected
		}
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		code := "UPSTREAM_ERROR"
		if resp.StatusCode == 403 || resp.StatusCode == 404 {
			code = "MEDIA_UNAVAILABLE"
		}
		return &Error{Code: code}
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return &Error{Code: "INVALID_MEDIA"}
	}
	if resp.ContentLength > MaxImageBytes {
		return &Error{Code: "MEDIA_TOO_LARGE"}
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, previewCacheEntryLimit+1))
	if readErr != nil {
		return &Error{Code: "UPSTREAM_UNAVAILABLE"}
	}
	if int64(len(prefix)) <= previewCacheEntryLimit {
		if resp.ContentLength >= 0 && int64(len(prefix)) != resp.ContentLength {
			return &Error{Code: "UPSTREAM_UNAVAILABLE"}
		}
		if _, ok := allowedPreviewType(contentType); ok && r.Context().Err() == nil {
			_ = p.previewCache.put(key, contentType, prefix)
		}
		writePreviewResponseHeaders(w, contentType, int64(len(prefix)), true)
		if _, err := w.Write(prefix); err != nil {
			return &Error{Code: "STREAM_FAILED", Started: true}
		}
		return nil
	}
	return p.forwardPreviewStream(w, resp, contentType, prefix)
}

func writePreviewResponseHeaders(w http.ResponseWriter, contentType string, length int64, known bool) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	if known {
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	}
	w.WriteHeader(http.StatusOK)
}

func (p *Proxy) forwardPreviewStream(w http.ResponseWriter, resp *http.Response, contentType string, prefix []byte) error {
	writePreviewResponseHeaders(w, contentType, resp.ContentLength, resp.ContentLength >= 0)
	n, err := w.Write(prefix)
	if err != nil {
		return &Error{Code: "STREAM_FAILED", Started: true}
	}
	written := int64(n)
	if written > MaxImageBytes {
		return &Error{Code: "MEDIA_TOO_LARGE", Started: true}
	}
	remaining := MaxImageBytes - written
	copied, err := io.CopyBuffer(w, io.LimitReader(resp.Body, remaining), make([]byte, 32<<10))
	written += copied
	if err != nil {
		return &Error{Code: "STREAM_FAILED", Started: true}
	}
	if written == MaxImageBytes {
		var extra [1]byte
		n, readErr := io.ReadFull(resp.Body, extra[:])
		if n > 0 {
			return &Error{Code: "MEDIA_TOO_LARGE", Started: true}
		}
		if readErr != nil && readErr != io.EOF {
			return &Error{Code: "STREAM_FAILED", Started: true}
		}
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return &Error{Code: "STREAM_FAILED", Started: true}
	}
	return nil
}
