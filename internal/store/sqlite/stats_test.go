package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestCatalogStatsEligiblePagesFollowDefaultSelectionPreferences(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 51, "approved")
	for _, spec := range []struct {
		id     int64
		rating string
	}{
		{511, "safe"}, {512, "safe"}, {513, "r18"},
	} {
		a := testArtwork(spec.id, 51)
		a.Kind = "illust"
		a.Rating = spec.rating
		a.AIStatus = "non_ai"
		if err := s.UpsertArtwork(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetImageFeedback(ctx, 512, 0, "dislike"); err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int64) {
		t.Helper()
		stats, err := s.CatalogStats(ctx)
		if err != nil || stats.ImagePages != 3 || stats.EligibleImagePages != want {
			t.Fatalf("stats=%+v err=%v, want %d eligible pages out of 3", stats, err, want)
		}
	}
	assertCount(1)

	rules := domain.DefaultRules()
	rules.AllowedRatings = append(rules.AllowedRatings, "r18")
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PutSetting(ctx, "preferences", raw, 0); err != nil {
		t.Fatal(err)
	}
	assertCount(2)
}
