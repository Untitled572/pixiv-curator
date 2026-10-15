// Package media forwards catalog images without a disk cache or account headers.
package media

import (
	"context"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxImageBytes int64 = 64 << 20

type Error struct {
	Code    string
	Started bool
}

func (e *Error) Error() string { return "media: " + e.Code }

type Proxy struct{ http *http.Client }

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
	return &Proxy{http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !domain.ValidImageURL(r.URL.String()) {
			return &Error{Code: "INVALID_TARGET"}
		}
		return nil
	}}}
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
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
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
