package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/pixiv"
)

func TestIncrementalKnownIllustrationPageContinuesToManga(t *testing.T) {
	ctx := context.Background()
	s, _ := openIngestTestStore(t)
	const artistID int64 = 401
	const knownID int64 = 4001
	const mangaID int64 = 4002
	catalog := NewCatalog(s)
	if err := catalog.ImportArtist(ctx, artistID); err != nil {
		t.Fatal(err)
	}
	jobID, err := catalog.DecideArtist(ctx, artistID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	known, err := pixiv.NormalizeArtwork(rawArtwork(knownID, artistID))
	if err != nil {
		t.Fatal(err)
	}
	known.Title = "already indexed illustration"
	if err = s.UpsertArtwork(ctx, known); err != nil {
		t.Fatal(err)
	}

	const illustPage = "https://app-api.pixiv.net/v1/user/illusts?filter=for_ios&type=illust&user_id=401&offset=30"
	var cursors []string
	client := &fakePixivClient{artist: func(_ context.Context, id int64, cursor string) (pixiv.ArtworkPage, error) {
		if id != artistID {
			t.Fatalf("artist request ID=%d; want %d", id, artistID)
		}
		cursors = append(cursors, cursor)
		switch len(cursors) {
		case 1:
			if cursor != "" {
				t.Fatalf("initial cursor=%q; want empty", cursor)
			}
			return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{rawArtwork(knownID, artistID)}, Next: illustPage}, nil
		case 2:
			u, parseErr := url.Parse(cursor)
			if parseErr != nil || u.Query().Get("type") != "manga" || u.Query().Get("offset") != "" {
				t.Fatalf("known illustration page should jump to manga start, cursor=%q err=%v", cursor, parseErr)
			}
			manga := rawArtwork(mangaID, artistID)
			manga.Type = "manga"
			manga.Title = "updated manga"
			return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{manga}}, nil
		default:
			t.Fatalf("unexpected artwork request %d", len(cursors))
			return pixiv.ArtworkPage{}, nil
		}
	}}
	job, err := s.Claim(ctx, time.Now())
	if err != nil || job.ID != jobID {
		t.Fatalf("claimed job=%+v err=%v; want approval job %q", job, err, jobID)
	}
	job.Payload = json.RawMessage(`{"artist_id":401,"phase":"incremental"}`)
	if err = NewIngest(s, client).Handlers()["sync_artist"](ctx, job); err != nil {
		t.Fatalf("incremental handler: %v", err)
	}
	if len(cursors) != 2 {
		t.Fatalf("requested cursors=%q; want illustration then manga", cursors)
	}
	artworks, err := catalog.Artworks(ctx, 0, 10, artistID)
	if err != nil {
		t.Fatal(err)
	}
	var title, kind string
	for _, artwork := range artworks {
		if artwork.ID == mangaID {
			title, kind = artwork.Title, artwork.Kind
		}
	}
	if title != "updated manga" || kind != "manga" {
		t.Fatalf("manga update not indexed: title=%q kind=%q", title, kind)
	}
}

func TestHistoryResumeUsesJobPayloadCursorAfterAnotherRunOverwritesArtistCursor(t *testing.T) {
	ctx := context.Background()
	s, _ := openIngestTestStore(t)
	const artistID int64 = 402
	catalog := NewCatalog(s)
	if err := catalog.ImportArtist(ctx, artistID); err != nil {
		t.Fatal(err)
	}
	otherJobID, err := catalog.DecideArtist(ctx, artistID, "approve")
	if err != nil {
		t.Fatal(err)
	}
	otherJob, err := s.Claim(ctx, time.Now())
	if err != nil || otherJob.ID != otherJobID {
		t.Fatalf("claimed other run=%+v err=%v", otherJob, err)
	}
	if err = s.CommitArtistBatch(ctx, otherJob, artistID, "other-run-id", "history", "cursor-from-other-run", nil); err != nil {
		t.Fatal(err)
	}
	const persistedCursor = "cursor-in-history-job"
	payload, err := json.Marshal(map[string]any{"artist_id": artistID, "phase": "history", "cursor": persistedCursor})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := s.Enqueue(ctx, "sync_artist", fmt.Sprintf("artist-history:%d", artistID), payload)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, time.Now())
	if err != nil || job.ID != jobID {
		t.Fatalf("claimed history run=%+v err=%v", job, err)
	}
	var requested string
	client := &fakePixivClient{artist: func(_ context.Context, id int64, cursor string) (pixiv.ArtworkPage, error) {
		if id != artistID {
			t.Fatalf("artist request ID=%d; want %d", id, artistID)
		}
		requested = cursor
		return pixiv.ArtworkPage{Artworks: []pixiv.RawArtwork{rawArtwork(4021, artistID)}}, nil
	}}
	if err = NewIngest(s, client).Handlers()["sync_artist"](ctx, job); err != nil {
		t.Fatalf("history handler: %v", err)
	}
	if requested != persistedCursor {
		t.Fatalf("history resumed at cursor %q; want durable job cursor %q", requested, persistedCursor)
	}
	finished, err := s.Job(ctx, job.ID)
	if err != nil || finished.State != "succeeded" {
		t.Fatalf("history job state=%+v err=%v", finished, err)
	}
}
