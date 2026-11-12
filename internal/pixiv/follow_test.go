package pixiv

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func readyFollowClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c := testClient(t, testTokenFile(t), rt)
	c.access = "fake-access"
	c.expires = time.Now().Add(time.Hour)
	return c
}

func TestFollowArtistUsesFixedPublicFormAndBearerHeader(t *testing.T) {
	var calls atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.String() != apiOrigin+"/v1/user/follow/add" {
			t.Errorf("request target = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer fake-access" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if len(form) != 2 || form.Get("user_id") != "987654321" || form.Get("restrict") != "public" {
			t.Errorf("form = %v", form)
		}
		if strings.Contains(string(body), "fake-access") || r.URL.RawQuery != "" {
			t.Error("credentials or follow fields leaked into URL/body")
		}
		return response(http.StatusOK, `{"error":false,"message":"ok","body":{}}`, nil), nil
	}))
	if err := c.FollowArtist(context.Background(), 987654321); err != nil {
		t.Fatalf("FollowArtist() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("API calls = %d, want 1", calls.Load())
	}
}

func TestFollowArtistCheckedUsesContextVisibility(t *testing.T) {
	c := readyFollowClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("user_id") != "124" || form.Get("restrict") != "private" {
			t.Errorf("private follow form = %v", form)
		}
		return response(http.StatusOK, `{}`, nil), nil
	}))
	ctx := domain.WithFollowVisibility(context.Background(), "private")
	if err := c.FollowArtistChecked(ctx, 124, nil); err != nil {
		t.Fatalf("private FollowArtistChecked() error = %v", err)
	}

	bad := domain.WithFollowVisibility(context.Background(), "friends")
	if err := c.FollowArtistChecked(bad, 125, nil); err == nil {
		t.Fatal("invalid visibility was accepted")
	}
}

func TestFollowArtistCheckedWaitsThenReturnsPreflightErrorWithoutPost(t *testing.T) {
	var requests atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusOK, `{}`, nil), nil
	}))
	c.nextRequest = time.Now().Add(35 * time.Millisecond)
	started := time.Now()
	sentinel := errors.New("preflight state changed")
	var checks atomic.Int32
	err := c.FollowArtistChecked(context.Background(), 101, func(context.Context) error {
		checks.Add(1)
		if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
			t.Errorf("preflight ran before pacing wait elapsed: %s", elapsed)
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("preflight error = %v, want original error identity", err)
	}
	if checks.Load() != 1 || requests.Load() != 0 {
		t.Fatalf("preflight checks/requests = %d/%d, want 1/0", checks.Load(), requests.Load())
	}
}

func TestFollowArtistCheckedCanCancelOrAllowOneWrite(t *testing.T) {
	var requests atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return response(http.StatusOK, `{}`, nil), nil
	}))
	var checks atomic.Int32
	err := c.FollowArtistChecked(context.Background(), 102, func(context.Context) error {
		checks.Add(1)
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || checks.Load() != 1 || requests.Load() != 0 {
		t.Fatalf("cancelled check error/checks/requests = %v/%d/%d", err, checks.Load(), requests.Load())
	}
	c.nextRequest = time.Time{}
	checks.Store(0)
	if err = c.FollowArtistChecked(context.Background(), 103, func(context.Context) error {
		checks.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("allowed follow error = %v", err)
	}
	if checks.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("allowed check/request count = %d/%d, want 1/1", checks.Load(), requests.Load())
	}
}

func TestFollowArtist429SharesCooldown(t *testing.T) {
	var calls atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusTooManyRequests, `{"error":true}`, http.Header{"Retry-After": []string{"23"}}), nil
	}))
	err := c.FollowArtist(context.Background(), 7)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.RetryAfter != 23*time.Second || apiErr.LocalCooldown {
		t.Fatalf("429 error = %+v", err)
	}
	c.nextRequest = time.Time{}
	err = c.FollowArtist(context.Background(), 8)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || !apiErr.LocalCooldown || apiErr.RetryAfter <= 0 {
		t.Fatalf("local cooldown error = %+v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls during cooldown = %d, want 1", calls.Load())
	}
}

func TestFollowArtistRefreshesOnceAfter401(t *testing.T) {
	var apiCalls, oauthCalls atomic.Int32
	var checks atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == oauthURL {
			oauthCalls.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("OAuth refresh unexpectedly received a bearer token")
			}
			return oauthResponse("access-new", ""), nil
		}
		call := apiCalls.Add(1)
		if call == 1 {
			if r.Header.Get("Authorization") != "Bearer fake-access" {
				t.Errorf("first Authorization = %q", r.Header.Get("Authorization"))
			}
			return response(http.StatusUnauthorized, "private body", nil), nil
		}
		if r.Header.Get("Authorization") != "Bearer access-new" {
			t.Errorf("retry Authorization = %q", r.Header.Get("Authorization"))
		}
		return response(http.StatusOK, `{"error":false}`, nil), nil
	}))
	if err := c.FollowArtistChecked(context.Background(), 71, func(context.Context) error {
		checks.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("FollowArtist() error = %v", err)
	}
	if apiCalls.Load() != 2 || oauthCalls.Load() != 1 {
		t.Fatalf("API/OAuth calls = %d/%d, want 2/1", apiCalls.Load(), oauthCalls.Load())
	}
	if checks.Load() != 2 {
		t.Fatalf("preflight checks=%d, want one before each POST", checks.Load())
	}
}

func TestFollowArtistCancellationInvalidIDsAndResponseErrors(t *testing.T) {
	var calls atomic.Int32
	c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, `{"error":true}`, nil), nil
	}))
	for _, id := range []int64{0, -1} {
		err := c.FollowArtist(context.Background(), id)
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != "INVALID_REQUEST" {
			t.Errorf("id %d error = %v", id, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid IDs made %d requests", calls.Load())
	}

	if err := c.FollowArtist(context.Background(), 72); err == nil {
		t.Fatal("API error payload was accepted")
	}
	c.nextRequest = time.Time{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c.nextRequest = time.Now().Add(time.Second)
	err := c.FollowArtist(ctx, 73)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled wait error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("API calls after cancellation = %d, want 1", calls.Load())
	}
}

func TestFollowArtistAcceptsEmptySuccessAndRejectsAPIErrorShapes(t *testing.T) {
	for _, body := range []string{`{}`, `{"message":"ok"}`, `{"error":false}`, `{"error":null}`, `{"error":{}}`} {
		t.Run("success "+body, func(t *testing.T) {
			c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, body, nil), nil
			}))
			if err := c.FollowArtist(context.Background(), 99); err != nil {
				t.Fatalf("valid success body %q returned error: %v", body, err)
			}
		})
	}
	for _, tc := range []struct {
		body string
		code string
	}{{`{"error":true}`, "UPSTREAM_ERROR"}, {`{"error":{"message":"failed"}}`, "UPSTREAM_ERROR"}, {`<html>error</html>`, "INVALID_RESPONSE"}, {`[]`, "INVALID_RESPONSE"}, {`null`, "INVALID_RESPONSE"}, {`{"error":"failed"}`, "INVALID_RESPONSE"}} {
		t.Run("invalid "+tc.body, func(t *testing.T) {
			c := readyFollowClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(http.StatusOK, tc.body, nil), nil
			}))
			err := c.FollowArtist(context.Background(), 99)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("body %q error=%v, want code %s", tc.body, err, tc.code)
			}
		})
	}
}
