package pixiv

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func readyFollowingClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	c := readyFollowClient(t, rt)
	c.accountID = 42
	return c
}

func TestFollowingRestrictedPaginatesBothVisibilityPhasesAsOwnAccount(t *testing.T) {
	for _, visibility := range []string{"public", "private"} {
		t.Run(visibility, func(t *testing.T) {
			var requests []string
			c := readyFollowingClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.String())
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fake-access" {
					t.Errorf("request method/auth = %s/%q", r.Method, r.Header.Get("Authorization"))
				}
				if len(requests) == 1 {
					return response(http.StatusOK, `{"user_previews":[{"user":{"id":77,"name":"follow"}}],"next_url":"https://app-api.pixiv.net/v1/user/following?offset=30"}`, nil), nil
				}
				return response(http.StatusOK, `{"user_previews":[{"user":{"id":78,"name":"next"}}],"next_url":""}`, nil), nil
			}))

			first, err := c.FollowingRestricted(context.Background(), "", visibility)
			if err != nil || len(first.Artists) != 1 || first.Artists[0].ID != 77 {
				t.Fatalf("first page=%+v err=%v", first, err)
			}
			parsed, _ := url.Parse(first.Next)
			if parsed.Path != "/v1/user/following" || parsed.Query().Get("user_id") != "42" || parsed.Query().Get("restrict") != visibility || parsed.Query().Get("offset") != "30" {
				t.Fatalf("normalized continuation = %q", first.Next)
			}
			second, err := c.FollowingRestricted(context.Background(), first.Next, visibility)
			if err != nil || len(second.Artists) != 1 || second.Artists[0].ID != 78 || second.Next != "" {
				t.Fatalf("second page=%+v err=%v", second, err)
			}
			if len(requests) != 2 {
				t.Fatalf("requests=%v", requests)
			}
			for _, raw := range requests {
				u, _ := url.Parse(raw)
				if u.Path != "/v1/user/following" || u.Query().Get("user_id") != "42" || u.Query().Get("restrict") != visibility {
					t.Errorf("unsafe following request URL %q", raw)
				}
			}
		})
	}
}

func TestFollowingRestrictedRejectsMismatchedContinuationWithoutAPIRequest(t *testing.T) {
	apiCalls := 0
	c := readyFollowingClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		apiCalls++
		return response(http.StatusOK, `{"user_previews":[]}`, nil), nil
	}))
	for _, next := range []string{
		"https://app-api.pixiv.net/v1/illust/following?user_id=42&restrict=public",
		"https://app-api.pixiv.net/v1/user/following?user_id=43&restrict=public&offset=1",
		"https://app-api.pixiv.net/v1/user/following?user_id=42&restrict=private&offset=1",
		"https://app-api.pixiv.net/v1/user/following?user_id=42&user_id=42&restrict=public",
		"https://app-api.pixiv.net/v1/user/following?user_id=42&restrict=public&restrict=private",
		"https://app-api.pixiv.net/v1/user/following?offset=30&phase=private",
		"https://app-api.pixiv.net.evil.invalid/v1/user/following?user_id=42&restrict=public",
	} {
		_, err := c.FollowingRestricted(context.Background(), next, "public")
		apiErr, ok := err.(*Error)
		if !ok || apiErr.Code != "INVALID_NEXT_URL" {
			t.Errorf("next %q error=%v, want INVALID_NEXT_URL", next, err)
		}
	}
	if _, err := c.FollowingRestricted(context.Background(), "", "friends"); err == nil {
		t.Fatal("unsupported visibility was accepted")
	}
	if apiCalls != 0 {
		t.Fatalf("invalid continuation made %d App API requests", apiCalls)
	}
}

func TestFollowingRestrictedRejectsMismatchedReturnedContinuation(t *testing.T) {
	c := readyFollowingClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"user_previews":[],"next_url":"https://app-api.pixiv.net/v1/user/following?user_id=99&restrict=private&offset=30"}`, nil), nil
	}))
	_, err := c.FollowingRestricted(context.Background(), "", "public")
	apiErr, ok := err.(*Error)
	if !ok || apiErr.Code != "INVALID_NEXT_URL" {
		t.Fatalf("mismatched upstream continuation error=%v", err)
	}
}

func TestFollowingRestrictedRejectsCanceledContext(t *testing.T) {
	apiCalls := 0
	c := readyFollowingClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		apiCalls++
		return response(http.StatusOK, `{"user_previews":[]}`, nil), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.FollowingRestricted(ctx, "", "private")
	if err == nil || !strings.Contains(err.Error(), "context canceled") || apiCalls != 0 {
		t.Fatalf("cancel error=%v API calls=%d", err, apiCalls)
	}
}
