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
	words []string
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
	first, second := directedArtwork(id*10+1, id), directedArtwork(id*10+2, id)
	if id == 4 {
		second.Width, second.Height = 1000, 2000
	}
	return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{first, second}}, nil
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
	if len(client.words) != 6 || client.words[0] != "original" || run.RequestCount != 8 {
		t.Fatalf("unexpected calls: queries=%v requests=%d", client.words, run.RequestCount)
	}
}
