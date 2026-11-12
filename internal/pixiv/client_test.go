package pixiv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testTokenFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "refresh-token")
	if err := os.WriteFile(path, []byte("refresh-old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func response(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func oauthResponse(access, refresh string) *http.Response {
	return response(200, fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_in":3600,"user":{"id":42}}`, access, refresh), nil)
}

func testClient(t *testing.T, tokenPath string, rt http.RoundTripper) *Client {
	t.Helper()
	return NewClient(tokenPath, OAuthConfig{ClientID: "fake-client", ClientSecret: "fake-secret", HashSecret: "fake-hash"}, rt)
}

func TestAccessTokenConcurrentRefreshSharedAndRotated(t *testing.T) {
	path := testTokenFile(t)
	var refreshes atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	c := testClient(t, path, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != oauthURL {
			t.Errorf("unexpected URL: %s", r.URL)
		}
		if r.FormValue("client_id") != "fake-client" || r.FormValue("refresh_token") != "refresh-old" {
			t.Errorf("unexpected fake OAuth form")
		}
		if refreshes.Add(1) == 1 {
			close(entered)
		}
		<-release
		return oauthResponse("access-new", "refresh-rotated"), nil
	}))
	const workers = 12
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			token, err := c.accessToken(context.Background(), "")
			if err == nil && token != "access-new" {
				err = fmt.Errorf("token=%q", token)
			}
			results <- err
		}()
	}
	<-entered
	time.Sleep(20 * time.Millisecond)
	close(release)
	for i := 0; i < workers; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("OAuth refreshes=%d, want 1", got)
	}
	got, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(got)) != "refresh-rotated" {
		t.Fatalf("rotated token not persisted: %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("token mode = %v, %v", st, err)
		}
	}
}

func TestRefreshPersistenceFailureBlocksFutureAttempts(t *testing.T) {
	path := testTokenFile(t)
	var refreshes atomic.Int32
	c := testClient(t, path, roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshes.Add(1)
		if err := os.Remove(path); err != nil {
			t.Errorf("remove token: %v", err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Errorf("replace credential with directory: %v", err)
		}
		return oauthResponse("access-new", "refresh-rotated"), nil
	}))
	for i := 0; i < 2; i++ {
		_, err := c.accessToken(context.Background(), "")
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "CREDENTIAL_SAVE_FAILED" {
			t.Fatalf("attempt %d error = %v", i, err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refreshes after blocked failure=%d, want 1", got)
	}
}

func TestGetRetriesUnauthorizedOnlyOnce(t *testing.T) {
	path := testTokenFile(t)
	var oauthCalls, apiCalls atomic.Int32
	c := testClient(t, path, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == oauthURL {
			oauthCalls.Add(1)
			return oauthResponse("access", ""), nil
		}
		apiCalls.Add(1)
		return response(401, `sensitive upstream body`, nil), nil
	}))
	var result map[string]any
	err := c.get(context.Background(), apiOrigin+"/v1/test", &result)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "AUTH_REQUIRED" {
		t.Fatalf("error = %v", err)
	}
	if apiCalls.Load() != 2 || oauthCalls.Load() != 2 {
		t.Fatalf("API/OAuth calls = %d/%d, want 2/2", apiCalls.Load(), oauthCalls.Load())
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	h := make(http.Header)
	h.Set("Retry-After", "17")
	got := responseError(response(429, "", h))
	if got.Code != "RATE_LIMITED" || got.RetryAfter != 17*time.Second {
		t.Fatalf("429 error = %+v", got)
	}
}

func TestOAuthRateLimitSharedAcrossJobs(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(429, "", http.Header{"Retry-After": []string{"17"}}), nil
	}))
	for i := 0; i < 2; i++ {
		_, err := c.accessToken(context.Background(), "")
		var limited *Error
		if !errors.As(err, &limited) || limited.Status != 429 || limited.RetryAfter <= 0 {
			t.Fatalf("cooldown error: %v", err)
		}
		if limited.LocalCooldown != (i == 1) {
			t.Fatalf("attempt %d local cooldown = %v", i, limited.LocalCooldown)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cooldown made %d upstream calls", calls.Load())
	}
	c.mu.Lock()
	c.cooldown = time.Now().Add(-time.Second)
	c.mu.Unlock()
	_, _ = c.accessToken(context.Background(), "")
	if calls.Load() != 2 {
		t.Fatal("expired cooldown did not permit retry")
	}
}

func TestNetworkErrorsDoNotExposeSensitiveDetails(t *testing.T) {
	path := testTokenFile(t)
	const secretText = "refresh-old"
	const upstreamText = "private proxy credential and upstream body"
	c := testClient(t, path, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(upstreamText + " " + secretText) }))
	_, err := c.accessToken(context.Background(), "")
	if err == nil || strings.Contains(err.Error(), secretText) || strings.Contains(err.Error(), upstreamText) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestRejectsInvalidNextURLAndOAuthRedirect(t *testing.T) {
	path := testTokenFile(t)
	var requests atomic.Int32
	c := testClient(t, path, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() == oauthURL {
			h := make(http.Header)
			h.Set("Location", "https://attacker.invalid/token")
			return response(302, "", h), nil
		}
		return response(200, `{"user_previews":[],"next_url":"https://attacker.invalid/steal"}`, nil), nil
	}))
	err := c.get(context.Background(), "https://app-api.pixiv.net.evil.invalid/v1/test", &map[string]any{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_NEXT_URL" || requests.Load() != 0 {
		t.Fatalf("invalid URL result: %v, requests=%d", err, requests.Load())
	}
	_, err = c.accessToken(context.Background(), "")
	if err == nil {
		t.Fatal("OAuth redirect was accepted")
	}
	if !errors.As(err, &apiErr) || apiErr.Code != "NETWORK_ERROR" || requests.Load() != 1 {
		t.Fatalf("redirect result: %v, requests=%d", err, requests.Load())
	}
}

func TestFollowingRejectsInvalidReturnedNextURL(t *testing.T) {
	path := testTokenFile(t)
	c := testClient(t, path, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == oauthURL {
			return oauthResponse("access", ""), nil
		}
		return response(200, `{"user_previews":[],"next_url":"https://attacker.invalid/next"}`, nil), nil
	}))
	_, err := c.Following(context.Background(), "")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_NEXT_URL" {
		t.Fatalf("following error = %v", err)
	}
}

func TestAccessTokenWaitCanBeCanceled(t *testing.T) {
	path := testTokenFile(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c := testClient(t, path, roundTripFunc(func(*http.Request) (*http.Response, error) {
		once.Do(func() { close(entered) })
		<-release
		return oauthResponse("access", ""), nil
	}))
	first := make(chan error, 1)
	go func() { _, err := c.accessToken(context.Background(), ""); first <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { _, err := c.accessToken(ctx, ""); second <- err }()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}
