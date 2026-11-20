package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/store/sqlite"
)

type directedTestClient struct {
	*fakeRecommendationClient
	words        []string
	rankingPages int
}

func directedArtwork(id, artistID int64, tags ...string) pixiv.RawArtwork {
	work := rawArtwork(id, artistID)
	work.Width, work.Height = 2000, 1000
	for _, tag := range tags {
		work.Tags = append(work.Tags, struct {
			Name string `json:"name"`
		}{tag})
	}
	return work
}

func (c *directedTestClient) SearchTaggedArtworks(_ context.Context, tag string, _ int) ([]pixiv.RawArtwork, error) {
	c.words = append(c.words, tag)
	adult := directedArtwork(31, 3, "original", "scenery")
	r18 := 1
	adult.XRestrict = &r18
	return []pixiv.RawArtwork{
		directedArtwork(11, 1, "original", "scenery"),
		directedArtwork(21, 2, "original"),
		adult,
		directedArtwork(41, 4, "original", "scenery"),
	}, nil
}

func (c *directedTestClient) ArtistArtworks(_ context.Context, id int64, _ string) (pixiv.ArtworkPage, error) {
	first := directedArtwork(id*10+1, id, "original", "scenery")
	second := directedArtwork(id*10+2, id, "original", "scenery")
	if id == 4 {
		second.Width, second.Height = 1000, 2000
		return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{first, second}}, nil
	}
	third := directedArtwork(id*10+3, id, "original", "scenery")
	otherTag := directedArtwork(id*10+4, id, "original")
	adult := directedArtwork(id*10+5, id, "original", "scenery")
	r18 := 1
	adult.XRestrict = &r18
	return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{first, second, third, otherTag, adult}}, nil
}

func (c *directedTestClient) ExplorationArtworks(_ context.Context, _, _ string, _ int) ([]pixiv.RawArtwork, error) {
	c.rankingPages++
	if c.rankingPages <= legacyDirectedPages {
		return nil, nil
	}
	// One ranking hit per artist. The other previews come from ArtistArtworks.
	works := make([]pixiv.RawArtwork, 0, 4)
	for i := range 4 {
		id := int64(100 + 4*c.rankingPages + i)
		works = append(works, directedArtwork(id*10+1, id))
	}
	return works, nil
}

func TestDirectedDiscoveryAppliesConditionsBeforeShowingCandidates(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "directed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &directedTestClient{fakeRecommendationClient: newFakeRecommendationClient()}
	service := NewRecommender(store, client)
	opts := DirectedDiscovery{Tags: []string{"original", "scenery"}, Rating: "safe", AI: "non_ai", Orientation: "landscape"}
	id, _, err := service.StartDirected(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Start(ctx); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mixed run: %v", err)
	}
	j, err := store.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Handler(ctx, j); err != nil {
		t.Fatal(err)
	}
	run, items, err := service.Candidates(ctx, id)
	if err != nil || run.State != "succeeded" || len(items) != 1 || items[0].ArtistID != 1 || items[0].Artist.ReviewStatus != "pending" {
		t.Fatalf("run=%+v items=%+v err=%v", run, items, err)
	}
	if len(client.words) != directedSearchPages || client.words[0] != "original" || run.RequestCount != directedSearchPages+2 {
		t.Fatalf("unexpected calls: queries=%v requests=%d", client.words, run.RequestCount)
	}
	assertDirectedSamples(t, items[0])
	for _, id := range items[0].SampleIDs {
		if id != "11" && id != "12" && id != "13" {
			t.Fatalf("sample outside the requested tags or rating: %s", id)
		}
	}
}

func TestDirectedRankingLooksPastSixPagesAndFillsThirtyCandidates(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "directed-ranking.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &directedTestClient{fakeRecommendationClient: newFakeRecommendationClient()}
	service := NewRecommender(store, client)
	id, _, err := service.StartDirected(ctx, DirectedDiscovery{Orientation: "landscape"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Handler(ctx, job); err != nil {
		t.Fatal(err)
	}
	run, items, err := service.Candidates(ctx, id)
	if err != nil || run.State != "succeeded" || len(items) != 30 {
		t.Fatalf("run state=%q candidates=%d err=%v", run.State, len(items), err)
	}
	if client.rankingPages != legacyDirectedPages+8 || run.RequestCount != client.rankingPages+30 {
		t.Fatalf("ranking pages=%d, requests=%d", client.rankingPages, run.RequestCount)
	}
	for _, item := range items {
		assertDirectedSamples(t, item)
	}
}

func assertDirectedSamples(t *testing.T, item domain.Recommendation) {
	t.Helper()
	if len(item.SampleIDs) != 3 {
		t.Fatalf("artist %d previews=%v, want three", item.ArtistID, item.SampleIDs)
	}
	seen := map[string]bool{}
	for _, id := range item.SampleIDs {
		if seen[id] {
			t.Fatalf("artist %d has duplicate preview %s", item.ArtistID, id)
		}
		seen[id] = true
	}
}
