package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestPageFeedbackFiltersOnlyTheDislikedPageAndFixedURL(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 901, "approved")
	seedSelectionArtwork(t, s, 902, 901, "safe", "non_ai", []string{"night"}, 2)
	if _, err := s.SetImageFeedback(ctx, 902, 0, "dislike"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligiblePage(ctx, 902, 0, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disliked fixed page error=%v", err)
	}
	if _, err := s.EligibleAdminPage(ctx, 902, 0); err != nil {
		t.Fatalf("feedback must not hide management history: %v", err)
	}
	for _, strategy := range []string{"random", "balanced"} {
		page, err := s.Select(ctx, domain.SelectionRequest{SelectionRules: domain.SelectionRules{Strategy: strategy}})
		if err != nil || page.PageIndex != 1 {
			t.Fatalf("strategy=%s got page=%+v error=%v", strategy, page, err)
		}
	}
	if _, err := s.SetImageFeedback(ctx, 902, 1, "dislike"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("all pages disliked error=%v", err)
	}
	if _, err := s.SetImageFeedback(ctx, 902, 0, "neutral"); err != nil {
		t.Fatal(err)
	}
	page, err := s.Select(ctx, domain.SelectionRequest{})
	if err != nil || page.PageIndex != 0 {
		t.Fatalf("neutral did not restore page: %+v %v", page, err)
	}
}

func TestFeedbackSupplyLotteryUsesTagEvidenceWithoutRelaxingFilters(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 910, "approved")
	seedEligibleArtist(t, s, 920, "approved")
	seedSelectionArtwork(t, s, 911, 910, "safe", "non_ai", []string{"blue"}, 1)
	seedSelectionArtwork(t, s, 921, 920, "safe", "non_ai", []string{"red"}, 1)
	if _, err := s.SetImageFeedback(ctx, 911, 0, "like"); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	counts, err := imageFeedbackInTx(ctx, tx)
	if err != nil || counts["blue"] != 1 {
		t.Fatalf("tag counts=%v err=%v", counts, err)
	}
	ids, err := feedbackOrderArtists(ctx, tx, []int64{910, 920}, counts)
	if err != nil || len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("weighted artists=%v %v", ids, err)
	}
	rows, err := selectionRows(ctx, tx, `ar.review_status='approved'`, nil, `a.id`, 20)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := feedbackOrderPages(ctx, tx, rows, counts, .5)
	if err != nil || len(pages) != 2 {
		t.Fatalf("weighted pages=%v %v", pages, err)
	}
}
