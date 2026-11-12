package pixiv

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestNormalizeArtist(t *testing.T) {
	var raw RawArtist
	if err := json.Unmarshal([]byte(`{"id":42,"name":"Artist","account":"artist","profile_image_urls":{"medium":"https://i.pximg.net/user-profile/img/2024/01/01/00/00/00/42_s.jpg"}}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtist(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.Name != "Artist" || got.Account != "artist" || got.AvatarURL == "" || got.ReviewStatus != "pending" || got.CollectionEnabled || got.RemoteFollowed {
		t.Fatalf("unexpected normalized artist: %+v", got)
	}
}

func TestNormalizeArtworkSinglePage(t *testing.T) {
	var raw RawArtwork
	data := `{"id":100,"user":{"id":42},"title":"one","caption":"caption","type":"illust","page_count":1,"width":800,"height":600,"x_restrict":0,"illust_ai_type":1,"tags":[{"name":"tag"}],"image_urls":{"medium":"https://i.pximg.net/c/540x540_70/img-master/img/2024/01/01/00/00/00/100_p0_master1200.jpg"},"meta_single_page":{"original_image_url":"https://i.pximg.net/img-original/img/2024/01/01/00/00/00/100_p0.jpg"}}`
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 100 || got.ArtistID != 42 || got.Kind != "illust" || got.Rating != "safe" || got.AIStatus != "non_ai" || got.MetadataState != "complete" {
		t.Fatalf("unexpected normalized artwork: %+v", got)
	}
	if len(got.Pages) != 1 || got.Pages[0].Index != 0 || got.Pages[0].OriginalURL == "" || got.Pages[0].PreviewURL == "" || got.Pages[0].Width != 800 || got.Pages[0].Height != 600 {
		t.Fatalf("unexpected normalized page: %+v", got.Pages)
	}
}

func TestNormalizeArtworkMultiPageAndUnknownEnums(t *testing.T) {
	var raw RawArtwork
	data := `{"id":101,"user":{"id":42},"type":"manga","page_count":2,"width":1200,"height":900,"x_restrict":9,"illust_ai_type":0,"meta_pages":[{"image_urls":{"original":"https://i.pximg.net/img-original/a_p0.jpg","medium":"https://i.pximg.net/img-master/a_p0.jpg"}},{"image_urls":{"original":"https://i.pximg.net/img-original/a_p1.jpg","medium":"https://i.pximg.net/img-master/a_p1.jpg"}}]}`
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "manga" || got.Rating != "unknown" || got.AIStatus != "unknown" || got.MetadataState != "complete" || len(got.Pages) != 2 || got.Pages[1].Index != 1 {
		t.Fatalf("unexpected normalized artwork: %+v", got)
	}

	var unknown RawArtwork
	if err := json.Unmarshal([]byte(`{"id":102,"user":{"id":42},"type":"future-kind","page_count":1,"meta_single_page":{"original_image_url":"https://i.pximg.net/img-original/b.jpg"}}`), &unknown); err != nil {
		t.Fatal(err)
	}
	got, err = NormalizeArtwork(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "unknown" || got.Rating != "unknown" || got.AIStatus != "unknown" {
		t.Fatalf("unknown values were assumed: %+v", got)
	}
}

func TestNormalizeArtworkMissingOriginalIsIncomplete(t *testing.T) {
	var raw RawArtwork
	if err := json.Unmarshal([]byte(`{"id":103,"user":{"id":42},"type":"ugoira","page_count":1}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.MetadataState == "complete" {
		t.Fatalf("artwork without a static original was marked complete: %+v", got)
	}
	if len(got.Pages) != 1 || got.Pages[0].OriginalURL != "" || got.Pages[0].State != "unavailable" {
		t.Fatalf("ugoira page must not be exposed as indexed static media: %+v", got.Pages)
	}
}

func TestNormalizeArtworkKnownRestrictions(t *testing.T) {
	for _, tc := range []struct {
		restrict int
		ai       int
		rating   string
		status   string
	}{
		{restrict: 1, ai: 2, rating: "r18", status: "ai"},
		{restrict: 2, ai: 1, rating: "r18g", status: "non_ai"},
	} {
		raw := RawArtwork{ID: 1, User: RawArtist{ID: 2}, XRestrict: &tc.restrict, IllustAIType: &tc.ai}
		got, err := NormalizeArtwork(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got.Rating != tc.rating || got.AIStatus != tc.status {
			t.Errorf("restrict=%d ai=%d mapped to %q/%q, want %q/%q", tc.restrict, tc.ai, got.Rating, got.AIStatus, tc.rating, tc.status)
		}
	}
}

func TestNormalizeArtworkCountsAndCreateDate(t *testing.T) {
	var raw RawArtwork
	if err := json.Unmarshal([]byte(`{"id":104,"user":{"id":42},"type":"illust","page_count":1,"total_bookmarks":17,"total_view":91,"create_date":"2024-01-02T03:04:05+08:00","meta_single_page":{"original_image_url":"https://i.pximg.net/img-original/c.jpg"}}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.FixedZone("UTC+8", 8*60*60)).UnixMilli()
	if got.Bookmarks == nil || *got.Bookmarks != 17 || got.Views == nil || *got.Views != 91 || got.PublishedAt == nil || *got.PublishedAt != expected {
		t.Fatalf("counts/date not normalized: bookmarks=%v views=%v published_at=%v", got.Bookmarks, got.Views, got.PublishedAt)
	}

	var missing RawArtwork
	if err := json.Unmarshal([]byte(`{"id":105,"user":{"id":42},"type":"illust","page_count":1,"create_date":"not-a-date","meta_single_page":{"original_image_url":"https://i.pximg.net/img-original/d.jpg"}}`), &missing); err != nil {
		t.Fatal(err)
	}
	got, err = NormalizeArtwork(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bookmarks != nil || got.Views != nil || got.PublishedAt != nil {
		t.Fatalf("missing or invalid optional fields should stay nil: bookmarks=%v views=%v published_at=%v", got.Bookmarks, got.Views, got.PublishedAt)
	}
}

func TestNormalizeArtworkRejectsExcessiveCounts(t *testing.T) {
	if _, err := NormalizeArtwork(RawArtwork{ID: 1, User: RawArtist{ID: 2}, PageCount: 1001}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("page_count error = %v, want ErrInvalid", err)
	}
	raw := RawArtwork{ID: 1, User: RawArtist{ID: 2}, Tags: make([]struct {
		Name string `json:"name"`
	}, 1001)}
	if _, err := NormalizeArtwork(raw); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("tag count error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRejectsInvalidIDsAndURLs(t *testing.T) {
	if _, err := NormalizeArtist(RawArtist{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("artist ID error = %v, want ErrInvalid", err)
	}
	if _, err := NormalizeArtwork(RawArtwork{ID: -1, User: RawArtist{ID: 2}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("artwork ID error = %v, want ErrInvalid", err)
	}
	if _, err := NormalizeArtist(RawArtist{ID: 1, ProfileImageURLs: struct {
		Medium string `json:"medium"`
	}{Medium: "https://example.com/avatar.jpg"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("avatar URL error = %v, want ErrInvalid", err)
	}
	bad := RawArtwork{ID: 1, User: RawArtist{ID: 2}, Type: "illust", PageCount: 1}
	bad.MetaSinglePage.OriginalImageURL = "https://example.com/image.jpg"
	if _, err := NormalizeArtwork(bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("original URL error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeArtistDefaultAvatar(t *testing.T) {
	raw := RawArtist{ID: 42, Name: "default avatar"}
	raw.ProfileImageURLs.Medium = "https://s.pximg.net/common/images/no_profile.png"
	got, err := NormalizeArtist(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.AvatarURL != raw.ProfileImageURLs.Medium || got.ID != raw.ID {
		t.Fatalf("artist identity or avatar changed: %+v", got)
	}
}

func TestNormalizeMultiPageUsesArtworkPreviewForFirstPage(t *testing.T) {
	raw := RawArtwork{ID: 106, User: RawArtist{ID: 42}, Type: "manga", PageCount: 2}
	raw.ImageURLs.Medium = "https://i.pximg.net/img-master/106_p0_master1200.jpg"
	if err := json.Unmarshal([]byte(`{"meta_pages":[{"image_urls":{"original":"https://i.pximg.net/img-original/106_p0.jpg"}},{"image_urls":{"original":"https://i.pximg.net/img-original/106_p1.jpg"}}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := NormalizeArtwork(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pages[0].PreviewURL != raw.ImageURLs.Medium {
		t.Fatal("first-page artwork preview was lost")
	}
	if got.Pages[1].PreviewURL != "" || got.Pages[1].OriginalURL == "" {
		t.Fatal("a preview for a different page must not be invented")
	}
}
