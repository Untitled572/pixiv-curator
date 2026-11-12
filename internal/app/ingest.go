package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"log/slog"
	"time"
)

type PixivClient interface {
	Following(context.Context, string) (pixiv.FollowingPage, error)
	ArtistArtworks(context.Context, int64, string) (pixiv.ArtworkPage, error)
	Artwork(context.Context, int64) (pixiv.RawArtwork, error)
	Status() string
	Credentials(context.Context, string) error
}
type IngestRepository interface {
	Enqueue(context.Context, string, string, json.RawMessage) (string, error)
	CommitFollowingBatch(context.Context, domain.Job, string, []domain.Artist, json.RawMessage, bool) error
	CommitArtistBatch(context.Context, domain.Job, int64, string, string, string, []domain.Artwork) error
	ArtistCursor(context.Context, int64, string) (string, error)
	CommitHydration(context.Context, domain.Job, domain.Artwork) error
	CommitUnavailable(context.Context, domain.Job, int64) error
	ResumeJob(context.Context, string) error
}
type Ingest struct {
	store  IngestRepository
	client PixivClient
}

func NewIngest(s IngestRepository, c PixivClient) *Ingest { return &Ingest{store: s, client: c} }
func (s *Ingest) Handlers() map[string]jobs.Handler {
	handlers := map[string]jobs.Handler{"sync_following": s.following, "sync_artist": s.artist, "hydrate_artwork": s.hydrate, "repair_media": s.hydrate}
	for kind, handler := range handlers {
		handlers[kind] = func(ctx context.Context, j domain.Job) error {
			var routing struct {
				AccountID string `json:"account_id"`
			}
			if json.Unmarshal(j.Payload, &routing) != nil {
				return taskInvalid()
			}
			return handler(domain.WithAccount(ctx, routing.AccountID), j)
		}
	}
	return handlers
}
func (s *Ingest) SyncFollowing(ctx context.Context) (string, error) {
	return s.store.Enqueue(ctx, "sync_following", "following:account", json.RawMessage(`{"cursor":""}`))
}
func (s *Ingest) AccountStatus() string { return s.client.Status() }
func (s *Ingest) Credentials(ctx context.Context, token string) error {
	return s.client.Credentials(ctx, token)
}
func (s *Ingest) Resume(ctx context.Context, id string) error {
	if s.client.Status() != "configured" && s.client.Status() != "ready" {
		return domain.ErrConflict
	}
	return s.store.ResumeJob(ctx, id)
}

func (s *Ingest) following(ctx context.Context, j domain.Job) error {
	var payload struct {
		Cursor    string `json:"cursor"`
		AccountID string `json:"account_id"`
		Restrict  string `json:"restrict"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil {
		return taskInvalid()
	}
	phase := payload.Restrict
	if phase == "" {
		phase = "public"
	}
	if !domain.ValidFollowVisibility(phase) {
		return taskInvalid()
	}
	restrictedClient, supportsRestricted := s.client.(interface {
		FollowingRestricted(context.Context, string, string) (pixiv.FollowingPage, error)
	})
	accountID := domain.ContextAccount(ctx)
	withRestrictedSnapshot := supportsRestricted && accountID != ""
	if phase == "private" && !withRestrictedSnapshot {
		return taskInvalid()
	}
	cursor := payload.Cursor
	// Public and private pages share one job/run. Only the final page of the
	// final phase completes the snapshot and marks unseen follows as removed.
	for {
		transitioned := false
		for pages := 0; pages < 10000; pages++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			var page pixiv.FollowingPage
			var err error
			if withRestrictedSnapshot {
				page, err = restrictedClient.FollowingRestricted(ctx, cursor, phase)
			} else {
				page, err = s.client.Following(ctx, cursor)
			}
			if err != nil {
				return classify(err)
			}
			if page.Next != "" && page.Next == cursor {
				return taskInvalid()
			}
			artists := make([]domain.Artist, 0, len(page.Artists))
			for _, raw := range page.Artists {
				a, err := pixiv.NormalizeArtist(raw)
				if err != nil {
					return classify(err)
				}
				artists = append(artists, a)
			}

			nextPhase, nextCursor := phase, page.Next
			complete := page.Next == ""
			if complete && phase == "public" && withRestrictedSnapshot {
				nextPhase, nextCursor, complete = "private", "", false
			}
			nextPayload, _ := json.Marshal(struct {
				Cursor   string `json:"cursor"`
				Account  string `json:"account_id"`
				Restrict string `json:"restrict"`
				Complete bool   `json:"complete,omitempty"`
			}{Cursor: nextCursor, Account: accountID, Restrict: nextPhase, Complete: complete})
			batchCtx := domain.WithFollowVisibility(ctx, phase)
			if err = s.store.CommitFollowingBatch(batchCtx, j, j.ID, artists, nextPayload, complete); err != nil {
				return classify(err)
			}
			slog.Info("following page indexed", "job_id", j.ID, "account_id", accountID, "restrict", phase, "items", len(artists), "complete", complete)
			if page.Next == "" {
				if phase == "public" && withRestrictedSnapshot {
					phase, cursor = "private", ""
					transitioned = true
					break
				}
				return nil
			}
			cursor = page.Next
		}
		if transitioned {
			continue
		}
		// The saved cursor remains in the current phase, so a retry resumes
		// exactly where this invocation stopped.
		return &jobs.JobError{Class: jobs.Failed, Code: "PAGE_BUDGET_EXCEEDED"}
	}
}
func (s *Ingest) artist(ctx context.Context, j domain.Job) error {
	var payload struct {
		ArtistID int64  `json:"artist_id"`
		Phase    string `json:"phase"`
		Cursor   string `json:"cursor"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.ArtistID <= 0 {
		return taskInvalid()
	}
	phase := payload.Phase
	if phase != "full" && phase != "incremental" && phase != "recent" && phase != "history" {
		return taskInvalid()
	}
	cursor, err := s.store.ArtistCursor(ctx, payload.ArtistID, j.ID)
	if err != nil {
		return classify(err)
	}
	if cursor == "" {
		cursor = payload.Cursor
	}
	processed := j.ProgressItems
	for pages := 0; pages < 10000; pages++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		page, err := s.client.ArtistArtworks(ctx, payload.ArtistID, cursor)
		if err != nil {
			return classify(err)
		}
		if page.Next != "" && page.Next == cursor {
			return taskInvalid()
		}
		works := make([]domain.Artwork, 0, len(page.Artworks))
		for _, raw := range page.Artworks {
			a, err := pixiv.NormalizeArtwork(raw)
			if err != nil {
				return classify(err)
			}
			if a.ArtistID != payload.ArtistID {
				return taskInvalid()
			}
			works = append(works, a)
		}
		if phase == "incremental" {
			if indexed, ok := s.store.(interface {
				KnownArtworkPage(context.Context, int64, []int64) (bool, error)
			}); ok {
				ids := make([]int64, 0, len(works))
				for _, work := range works {
					ids = append(ids, work.ID)
				}
				known, err := indexed.KnownArtworkPage(ctx, payload.ArtistID, ids)
				if err != nil {
					return classify(err)
				}
				if known {
					page.Next, err = pixiv.NextArtworkSection(cursor, page.Next)
					if err != nil {
						return classify(err)
					}
				}
			}
		}
		if err = s.store.CommitArtistBatch(ctx, j, payload.ArtistID, j.ID, phase, page.Next, works); err != nil {
			return classify(err)
		}
		slog.Info("artwork page indexed", "job_id", j.ID, "artist_id", payload.ArtistID, "phase", phase, "items", len(works))
		processed += int64(len(works))
		if page.Next == "" || ((phase == "recent" || phase == "incremental") && processed >= 100) {
			return nil
		}
		cursor = page.Next
	}
	return &jobs.JobError{Class: jobs.Failed, Code: "PAGE_BUDGET_EXCEEDED"}
}
func (s *Ingest) hydrate(ctx context.Context, j domain.Job) error {
	var payload struct {
		ID int64 `json:"illust_id"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.ID <= 0 {
		return taskInvalid()
	}
	raw, err := s.client.Artwork(ctx, payload.ID)
	if err != nil {
		var upstream *pixiv.Error
		if errors.As(err, &upstream) && upstream.Status == 404 {
			return classify(s.store.CommitUnavailable(ctx, j, payload.ID))
		}
		return classify(err)
	}
	if raw.ID != payload.ID {
		return taskInvalid()
	}
	a, err := pixiv.NormalizeArtwork(raw)
	if err != nil {
		return classify(err)
	}
	return classify(s.store.CommitHydration(ctx, j, a))
}
func taskInvalid() error { return &jobs.JobError{Class: jobs.Failed, Code: "INVALID_TASK"} }
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, domain.ErrInvalid) {
		return taskInvalid()
	}
	if errors.Is(err, domain.ErrConflict) {
		return &jobs.JobError{Class: jobs.Failed, Code: "ARTIST_STATE_CHANGED"}
	}
	var e *pixiv.Error
	if errors.As(err, &e) {
		switch {
		case e.Status == 401 || e.Code == "CREDENTIAL_SAVE_FAILED" || e.Code == "ACCOUNT_NOT_CONFIGURED":
			return &jobs.JobError{Class: jobs.PausedAuth, Code: e.Code}
		case e.Status == 404:
			return &jobs.JobError{Class: jobs.Failed, Code: e.Code}
		case e.Status == 429:
			class := jobs.Retry
			if e.LocalCooldown {
				class = jobs.Deferred
			}
			return &jobs.JobError{Class: class, Code: e.Code, RetryAfter: e.RetryAfter}
		case e.Code == "INVALID_NEXT_URL" || e.Code == "INVALID_RESPONSE":
			return &jobs.JobError{Class: jobs.Failed, Code: e.Code}
		default:
			return &jobs.JobError{Class: jobs.Retry, Code: e.Code}
		}
	}
	return &jobs.JobError{Class: jobs.Retry, Code: "STORE_ERROR", Err: fmt.Errorf("catalog operation failed")}
}

// SyncInterval is the initial conservative scheduler interval.
const SyncInterval = 6 * time.Hour
