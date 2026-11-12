package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestRequestOverridesR18KeepHardExclusions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 9100, "approved")
	seedEligibleArtist(t, s, 9101, "blocked")
	seedSelectionArtwork(t, s, 9110, 9100, "r18", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 9111, 9100, "r18g", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 9112, 9101, "r18", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 9113, 9100, "r18", "non_ai", nil, 1)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artwork_preferences(illust_id,value,updated_at) VALUES(9113,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	putSelectionRules(t, s, domain.DefaultRules())
	req := domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"r18", "r18g"}}, Overrides: map[string]bool{"r18": true}}
	got, err := s.Select(ctx, req)
	if err != nil || (got.IllustID != 9110 && got.IllustID != 9111) {
		t.Fatalf("explicit R18 selection=%+v err=%v", got, err)
	}
	for _, id := range []int64{9112, 9113} {
		if _, err := s.EligiblePageWithRequest(ctx, id, 0, req); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("fixed page %d error=%v, want ErrNotFound", id, err)
		}
	}
}

func TestRequestOverrideCanIncludeMangaAndFixedPageUsesIt(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 9200, "approved")
	seedSelectionArtwork(t, s, 9210, 9200, "safe", "non_ai", nil, 1)
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET kind='manga' WHERE id=9210`); err != nil {
		t.Fatal(err)
	}
	putSelectionRules(t, s, domain.DefaultRules())
	if _, err := s.EligiblePageWithRequest(ctx, 9210, 0, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("default fixed manga page error=%v, want ErrNotFound", err)
	}
	req := domain.SelectionRequest{SelectionRules: domain.SelectionRules{ExcludeManga: false}, Overrides: map[string]bool{"exclude_manga": true}}
	if _, err := s.EligiblePageWithRequest(ctx, 9210, 0, req); err != nil {
		t.Fatalf("explicit manga fixed page: %v", err)
	}
	got, err := s.Select(ctx, req)
	if err != nil || got.IllustID != 9210 {
		t.Fatalf("explicit manga selection=%+v err=%v", got, err)
	}
}

func TestRequestOrientationOverrideUsesSquareRatioBoundaries(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 9300, "approved")
	dimensions := []struct {
		id            int64
		width, height int
		orientation   string
		wantLandscape bool
	}{
		{9301, 1000, 950, "landscape", false},
		{9302, 1100, 1000, "landscape", false},
		{9303, 1101, 1000, "landscape", true},
		{9304, 1000, 1100, "portrait", false},
		{9305, 1000, 1101, "portrait", true},
	}
	for _, tc := range dimensions {
		seedSelectionArtwork(t, s, tc.id, 9300, "safe", "non_ai", nil, 1)
		if _, err := s.db.ExecContext(ctx, `UPDATE image_pages SET width=?,height=? WHERE illust_id=? AND page_index=0`, tc.width, tc.height, tc.id); err != nil {
			t.Fatal(err)
		}
	}
	putSelectionRules(t, s, domain.DefaultRules())
	for _, tc := range dimensions {
		req := domain.SelectionRequest{SelectionRules: domain.SelectionRules{Orientation: tc.orientation}, Overrides: map[string]bool{"orientation": true}}
		_, err := s.EligiblePageWithRequest(ctx, tc.id, 0, req)
		if tc.wantLandscape && err != nil {
			t.Errorf("%s page %d rejected: %v", tc.orientation, tc.id, err)
		}
		if !tc.wantLandscape && !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("square page %d orientation=%s error=%v; want ErrNotFound", tc.id, tc.orientation, err)
		}
	}
}

func TestRequestDefaultsStillExcludeUnknown(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 9400, "approved")
	seedSelectionArtwork(t, s, 9410, 9400, "unknown", "non_ai", nil, 1)
	putSelectionRules(t, s, domain.DefaultRules())
	if _, err := s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("default unknown selection error=%v, want ErrNoMatch", err)
	}
	if _, err := s.EligiblePageWithRequest(ctx, 9410, 0, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("default unknown fixed page error=%v, want ErrNotFound", err)
	}
}
