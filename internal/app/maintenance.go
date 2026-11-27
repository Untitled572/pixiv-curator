package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type MaintenanceRepository interface {
	collectionScheduleReader
	Artists(context.Context, int64, int) ([]domain.Artist, error)
	Enqueue(context.Context, string, string, json.RawMessage) (string, error)
	CleanupSelectionHistory(context.Context) error
	ReclaimFreePages(context.Context) error
}

type collectionScheduleReader interface {
	Setting(context.Context, string) (json.RawMessage, int64, error)
}

func ReadCollectionSchedule(ctx context.Context, reader collectionScheduleReader) (domain.CollectionSchedule, int64, error) {
	raw, version, err := reader.Setting(ctx, "sync")
	if errors.Is(err, domain.ErrNotFound) {
		return domain.CollectionSchedule{IntervalHours: domain.DefaultCollectionIntervalHours}, 0, nil
	}
	if err != nil {
		return domain.CollectionSchedule{}, 0, err
	}
	var schedule domain.CollectionSchedule
	if err = json.Unmarshal(raw, &schedule); err != nil || !schedule.Valid() {
		return domain.CollectionSchedule{}, 0, domain.ErrInvalid
	}
	return schedule, version, nil
}

// RunMaintenance checks the stored interval each minute starting from launch.
func RunMaintenance(ctx context.Context, s MaintenanceRepository, onError func(error)) {
	history := time.NewTicker(time.Minute)
	defer history.Stop()
	lastScheduled := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-history.C:
			if err := s.CleanupSelectionHistory(ctx); err != nil {
				onError(err)
			}
			if err := s.ReclaimFreePages(ctx); err != nil {
				onError(err)
			}
			schedule, _, err := ReadCollectionSchedule(ctx, s)
			if err != nil {
				onError(err)
				continue
			}
			if now.Sub(lastScheduled) < time.Duration(schedule.IntervalHours)*time.Hour {
				continue
			}
			lastScheduled = now
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
