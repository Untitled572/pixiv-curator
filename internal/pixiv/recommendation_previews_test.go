package pixiv

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelatedArtistPreviewsDecodesOfficialUserPreviewShape(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, "", nil)
	c.access = "access-test"
	c.expires = time.Now().Add(time.Hour)
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "app-api.pixiv.net" || r.URL.Path != "/v1/user/related" {
			t.Errorf("request target = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer access-test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("filter") != "for_ios" || r.URL.Query().Get("offset") != "0" || r.URL.Query().Get("seed_user_id") != "42" {
			t.Errorf("related query = %v", r.URL.Query())
		}
		return response(http.StatusOK, `{"user_previews":[{"user":{"id":71,"name":"related"},"illusts":[{"id":7101,"user":{"id":71},"page_count":0,"tags":[]}]},{"user":{"id":72},"illusts":[]}]}`, nil), nil
	})
	previews, err := c.RelatedArtistPreviews(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(previews) != 2 || previews[0].User.ID != 71 || len(previews[0].Illusts) != 1 || previews[0].Illusts[0].ID != 7101 || previews[0].Illusts[0].User.ID != 71 {
		t.Fatalf("previews=%+v calls=%d", previews, calls.Load())
	}
	if previews[1].User.ID != 72 || len(previews[1].Illusts) != 0 {
		t.Fatalf("empty preview = %+v", previews[1])
	}
}

func TestRelatedArtistPreviewsRejectsInvalidIDAndCancelsWait(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, "", roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(http.StatusOK, `{"user_previews":[]}`, nil), nil
	}))
	if _, err := c.RelatedArtistPreviews(context.Background(), 0); err == nil {
		t.Fatal("zero seed ID accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.nextRequest = time.Now().Add(time.Hour)
	_, err := c.RelatedArtistPreviews(ctx, 42)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("requests = %d, want 0", calls.Load())
	}
}
