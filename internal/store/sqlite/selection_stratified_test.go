package sqlite

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestStratifiedWeightingAndPageDamping(t *testing.T) {
	cases := []struct {
		name                 string
		pages                int
		multiplier, expected float64
	}{
		{"neutral multi-page artist", 25, 1, .5},
		{"single-work cooldown", 1, .25, .2},
		{"uniform work multiplier", 1, 1.35, 1.35 / 2.35},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &stratifiedWork{id: 1, artist: 1, multiplier: tc.multiplier}
			for i := 0; i < tc.pages; i++ {
				a.pages = append(a.pages, i)
			}
			b := &stratifiedWork{id: 2, artist: 2, multiplier: 1, pages: []int{0}}
			const n = 30000
			count := 0
			for i := 0; i < n; i++ {
				if stratifiedLottery([]*stratifiedWork{a, b}, nil, nil).page.ArtistID == 1 {
					count++
				}
			}
			observed := float64(count) / n
			if math.Abs(observed-tc.expected) > .02 {
				t.Fatalf("observed %.3f, expected %.3f", observed, tc.expected)
			}
		})
	}
}
func TestCooldownAnchors(t *testing.T) {
	now := time.Unix(2000000, 0)
	for _, tc := range []struct {
		age  time.Duration
		want float64
	}{{0, .25}, {24 * time.Hour, .4}, {7 * 24 * time.Hour, 1}} {
		got := cooldownFactor(now, now.Add(-tc.age).UnixMilli(), 24*time.Hour, 7*24*time.Hour, .25, .4)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("age %v: got %v want %v", tc.age, got, tc.want)
		}
	}
	if got := cooldownFactor(now, 0, 24*time.Hour, 7*24*time.Hour, .25, .4); got != 1 {
		t.Fatalf("missing exposure: %v", got)
	}
}

func TestStratifiedSelectionUsesShortRecheck(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 8201, "approved")
	seedSelectionArtwork(t, s, 8202, 8201, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 8203, 8201, "safe", "non_ai", nil, 1)
	req := domain.SelectionRequest{SelectionRules: domain.SelectionRules{Strategy: "stratified"}}
	a, err := s.Select(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Select(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.IllustID == b.IllustID {
		t.Fatal("recent artwork repeated while alternative exists")
	}
	c, err := s.Select(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Repeated {
		t.Fatal("expected fallback after exhausting eligible works")
	}
	var n int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM distribution_history`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("history=%d err=%v", n, err)
	}
}

func TestConcurrentStratifiedAvoidsSameWork(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 8301, "approved")
	seedSelectionArtwork(t, s, 8302, 8301, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 8303, 8301, "safe", "non_ai", nil, 1)
	req := domain.SelectionRequest{SelectionRules: domain.SelectionRules{Strategy: "stratified"}}
	type result struct {
		p domain.SelectedPage
		e error
	}
	ch := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { p, e := s.Select(ctx, req); ch <- result{p, e} }()
	}
	a, b := <-ch, <-ch
	if a.e != nil || b.e != nil {
		t.Fatalf("selection errors: %v %v", a.e, b.e)
	}
	if a.p.IllustID == b.p.IllustID {
		t.Fatalf("same artwork selected concurrently: %d", a.p.IllustID)
	}
}
