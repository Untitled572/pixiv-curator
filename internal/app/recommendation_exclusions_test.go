package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/recommend"
)

func TestRecommendationRefreshSkipsPreviousPendingCandidatesBeforeTightRecallCap(t *testing.T) {
	ctx := context.Background()
	s := openRecommendationAppStore(t)
	const seedID int64 = 74000
	addApprovedRecommendationSeed(t, s, seedID)
	if err := s.UpsertArtwork(ctx, storedArtwork(74001, seedID, "seed-tag")); err != nil {
		t.Fatal(err)
	}
	const previousBase int64 = 75000
	const nextBase int64 = 76000
	var previewCalls int
	base := newFakeRecommendationClient()
	base.relatedArtworks = func(context.Context, int64) ([]pixiv.RawArtwork, error) { return nil, nil }
	client := &fakeRecommendationPreviewClient{
		fakeRecommendationClient: base,
		previews: func(_ context.Context, id int64) ([]pixiv.UserPreview, error) {
			if id != seedID {
				t.Errorf("preview seed=%d, want %d", id, seedID)
			}
			previewCalls++
			count := 30
			if previewCalls == 2 {
				count = 60
			}
			out := make([]pixiv.UserPreview, 0, count)
			for i := 0; i < count; i++ {
				artistID := previousBase + int64(i)
				if previewCalls == 2 && i >= 30 {
					artistID = nextBase + int64(i-30)
				}
				illustID := artistID*10 + 1
				out = append(out, pixiv.UserPreview{
					User:    rawArtist(artistID, fmt.Sprintf("candidate-%d", artistID)),
					Illusts: []pixiv.RawArtwork{recArtwork(illustID, artistID, "seed-tag")},
				})
			}
			return out, nil
		},
	}
	budget := recommend.Budget{
		SeedArtists: 1, SeedSamplesPerArtist: 1,
		CandidateArtists: 30, CandidateSamplesPerArtist: 1,
		RecallArtists: 30, ExplorationArtists: 0, TotalRequests: 80,
	}
	service := NewRecommender(s, client)

	firstRun, firstJob := createRecommendationJob(t, s, budget)
	if err := service.Handler(ctx, firstJob); err != nil {
		t.Fatal(err)
	}
	first, err := s.RecommendationCandidates(ctx, firstRun, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 30 {
		t.Fatalf("first run candidates=%d, want 30", len(first))
	}
	previous := make(map[int64]bool, len(first))
	for _, item := range first {
		previous[item.ArtistID] = true
		if item.Artist.ReviewStatus != "pending" || item.Artist.CollectionEnabled {
			t.Fatalf("first run automatically approved candidate: %+v", item.Artist)
		}
	}

	secondRun, secondJob := createRecommendationJob(t, s, budget)
	if secondRun == firstRun {
		t.Fatal("manual refresh reused the previous completed run")
	}
	if err = service.Handler(ctx, secondJob); err != nil {
		t.Fatal(err)
	}
	second, err := s.RecommendationCandidates(ctx, secondRun, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 30 {
		t.Fatalf("second run candidates=%d, want 30", len(second))
	}
	for _, item := range second {
		if previous[item.ArtistID] {
			t.Fatalf("previous candidate %d repeated in refresh", item.ArtistID)
		}
		if item.ArtistID < nextBase || item.ArtistID >= nextBase+30 {
			t.Fatalf("unexpected refreshed candidate ID=%d", item.ArtistID)
		}
		if item.Artist.ReviewStatus != "pending" || item.Artist.CollectionEnabled {
			t.Fatalf("refreshed candidate automatically approved: %+v", item.Artist)
		}
	}
	if previewCalls != 2 {
		t.Fatalf("preview calls=%d, want one per run", previewCalls)
	}
	for _, item := range first {
		artist, err := NewCatalog(s).Artist(ctx, item.ArtistID)
		if err != nil {
			t.Fatal(err)
		}
		if artist.ReviewStatus != "pending" || artist.CollectionEnabled {
			t.Fatalf("previous pending candidate changed after refresh: %+v", artist)
		}
	}
}
