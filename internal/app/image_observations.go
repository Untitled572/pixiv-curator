package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type imageTransferRepository interface {
	RecordImageTransfers(context.Context, []domain.ImageTransfer) error
}

// StartImageObservations begins bounded batched history and dimension writes.
func (s *Images) StartImageObservations(ctx context.Context) <-chan struct{} {
	s.observations = make(chan domain.ImageTransfer, 128)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case event := <-s.observations:
				events := []domain.ImageTransfer{event}
				for len(events) < 32 && len(s.observations) > 0 {
					events = append(events, <-s.observations)
				}
				s.writeObservations(events)
			case <-ctx.Done():
				var remaining []domain.ImageTransfer
				for len(s.observations) > 0 {
					remaining = append(remaining, <-s.observations)
				}
				if len(remaining) > 0 {
					s.writeObservations(remaining)
				}
				return
			}
		}
	}()
	return done
}

func (s *Images) ObserveTransfer(page domain.SelectedPage, width, height int) {
	if page.SelectionID == "" && (width == 0 || height == 0) {
		return
	}
	event := domain.ImageTransfer{SelectionID: page.SelectionID, IllustID: page.IllustID, PageIndex: page.PageIndex,
		OriginalURL: page.OriginalURL, Width: width, Height: height, At: time.Now().UnixMilli()}
	if s.observations != nil {
		select {
		case s.observations <- event:
			return
		default:
		}
	}
	s.writeObservations([]domain.ImageTransfer{event})
}

func (s *Images) writeObservations(events []domain.ImageTransfer) {
	repository, ok := s.store.(imageTransferRepository)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := repository.RecordImageTransfers(ctx, events); err != nil {
		slog.Warn("image transfer metadata write failed")
	}
}
