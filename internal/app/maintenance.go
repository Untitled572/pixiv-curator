package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"time"
)

type MaintenanceRepository interface {
	Artists(context.Context, int64, int) ([]domain.Artist, error)
	Enqueue(context.Context, string, string, json.RawMessage) (string, error)
	CleanupSelectionHistory(context.Context) error
}

// RunMaintenance starts periodic work after the first interval, giving newly
// installed accounts time for a small initial collection before the full schedule.
func RunMaintenance(ctx context.Context, s MaintenanceRepository, onError func(error)) {
	history := time.NewTicker(time.Minute)
	defer history.Stop()
	syncTick := time.NewTicker(SyncInterval)
	defer syncTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-history.C:
			if err := s.CleanupSelectionHistory(ctx); err != nil {
				onError(err)
			}
		case <-syncTick.C:
			if accounts, ok := s.(interface {
				Accounts(context.Context) ([]domain.Account, error)
				SyncAccount(context.Context, string) (string, error)
			}); ok {
				list, err := accounts.Accounts(ctx)
				if err != nil {
					onError(err)
				} else {
					for _, a := range list {
						if _, err = accounts.SyncAccount(ctx, a.ID); err != nil {
							onError(err)
						}
					}
				}
			}
			if err := scheduleArtists(ctx, s); err != nil {
				onError(err)
			}
		}
	}
}
func scheduleArtists(ctx context.Context, s MaintenanceRepository) error {
	after := int64(0)
	for {
		artists, err := s.Artists(ctx, after, 200)
		if err != nil {
			return err
		}
		for _, a := range artists {
			after = a.ID
			if a.ReviewStatus != "approved" || !a.CollectionEnabled {
				continue
			}
			payload, _ := json.Marshal(map[string]any{"artist_id": a.ID, "phase": "incremental"})
			if _, err = s.Enqueue(ctx, "sync_artist", fmt.Sprintf("artist:%d", a.ID), payload); err != nil {
				return err
			}
		}
		if len(artists) < 200 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
