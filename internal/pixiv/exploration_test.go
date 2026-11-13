package pixiv

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestExplorationArtworksBuildsBoundedRankingRequest(t *testing.T) {
	path := testTokenFile(t)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	var got *http.Request
	c := testClient(t, path, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, `{"illusts":[{"id":42}]}`, nil), nil
	}))
	c.access, c.expires = "test-access", time.Now().Add(time.Hour)
	items, err := c.ExplorationArtworks(context.Background(), "week_rookie", yesterday, 90)
	if err != nil || len(items) != 1 || items[0].ID != 42 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if got == nil || got.URL.Scheme != "https" || got.URL.Host != "app-api.pixiv.net" || got.URL.Path != "/v1/illust/ranking" {
		t.Fatalf("request URL=%v", got)
	}
	q := got.URL.Query()
	if q.Get("mode") != "week_rookie" || q.Get("date") != yesterday || q.Get("offset") != "90" || q.Get("filter") != "for_ios" {
		t.Fatalf("query=%v", q)
	}
}

func TestExplorationArtworksRejectsInvalidBoundsWithoutNetwork(t *testing.T) {
	calls := 0
	c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"illusts":[]}`, nil), nil
	}))
	c.access, c.expires = "test-access", time.Now().Add(time.Hour)
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	for _, tc := range []struct {
		mode, date string
		offset     int
	}{{"invalid", yesterday, 0}, {"day", "2026-02-30", 0}, {"day", today, 0}, {"day", "9999-12-31", 0}, {"day", yesterday, -30}, {"day", yesterday, 15}, {"day", yesterday, 480}} {
		if _, err := c.ExplorationArtworks(context.Background(), tc.mode, tc.date, tc.offset); err == nil {
			t.Errorf("accepted mode=%q date=%q offset=%d", tc.mode, tc.date, tc.offset)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs made %d requests", calls)
	}
}

func TestExplorationArtworksHonorsCanceledContext(t *testing.T) {
	calls := 0
	c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"illusts":[]}`, nil), nil
	}))
	c.access, c.expires = "test-access", time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	_, err := c.ExplorationArtworks(ctx, "day", yesterday, 0)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
