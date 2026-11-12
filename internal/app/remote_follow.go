package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/jobs"
)

type RemoteFollowRepository interface {
	NeedsRemoteFollow(context.Context, domain.Job, string, int64, string) (bool, error)
	CommitRemoteFollow(context.Context, domain.Job, string, int64, string) error
}

var errFollowNoLongerNeeded = errors.New("follow intent no longer active")

type RemoteFollowClient interface {
	FollowArtist(context.Context, int64) error
}
type RemoteFollows struct {
	store  RemoteFollowRepository
	client RemoteFollowClient
}

func NewRemoteFollows(store RemoteFollowRepository, client RemoteFollowClient) *RemoteFollows {
	return &RemoteFollows{store: store, client: client}
}
func (s *RemoteFollows) Handler(ctx context.Context, j domain.Job) error {
	var payload struct {
		AccountID string `json:"account_id"`
		ArtistID  int64  `json:"artist_id"`
		Origin    string `json:"origin"`
		Restrict  string `json:"restrict"`
	}
	if json.Unmarshal(j.Payload, &payload) != nil || payload.AccountID == "" || payload.ArtistID <= 0 || (payload.Origin != "copy" && payload.Origin != "approval") {
		return taskInvalid()
	}
	if payload.Restrict == "" {
		payload.Restrict = "public"
	}
	if !domain.ValidFollowVisibility(payload.Restrict) {
		return taskInvalid()
	}
	needed, err := s.store.NeedsRemoteFollow(ctx, j, payload.AccountID, payload.ArtistID, payload.Origin)
	if errors.Is(err, domain.ErrNotFound) {
		return &jobs.JobError{Class: jobs.Failed, Code: "FOLLOW_TARGET_UNAVAILABLE"}
	}
	if err != nil {
		return classifyFollowError(err)
	}
	if !needed {
		return nil
	}
	accountCtx := domain.WithFollowVisibility(domain.WithAccount(ctx, payload.AccountID), payload.Restrict)
	if checked, ok := s.client.(interface {
		FollowArtistChecked(context.Context, int64, func(context.Context) error) error
	}); ok {
		err = checked.FollowArtistChecked(accountCtx, payload.ArtistID, func(checkCtx context.Context) error {
			stillNeeded, checkErr := s.store.NeedsRemoteFollow(checkCtx, j, payload.AccountID, payload.ArtistID, payload.Origin)
			if checkErr != nil {
				return checkErr
			}
			if !stillNeeded {
				return errFollowNoLongerNeeded
			}
			return nil
		})
	} else {
		err = s.client.FollowArtist(accountCtx, payload.ArtistID)
	}
	if errors.Is(err, errFollowNoLongerNeeded) {
		return nil
	}
	if err != nil {
		return classifyFollowError(err)
	}
	return classifyFollowError(s.store.CommitRemoteFollow(ctx, j, payload.AccountID, payload.ArtistID, payload.Origin))
}

func classifyFollowError(err error) error {
	if errors.Is(err, domain.ErrLease) {
		return &jobs.JobError{Class: jobs.Failed, Code: "FOLLOW_INTENT_CANCELED", Err: err}
	}
	return classify(err)
}
