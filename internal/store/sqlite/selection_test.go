package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

func TestSelectAppliesHardFiltersAndRuleIntersection(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 700, "approved")
	seedEligibleArtist(t, s, 701, "pending")
	seedEligibleArtist(t, s, 702, "rejected")
	seedEligibleArtist(t, s, 703, "blocked")

	seedSelectionArtwork(t, s, 710, 700, "safe", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 711, 700, "safe", "non_ai", []string{"azure", "night"}, 1)
	seedSelectionArtwork(t, s, 712, 700, "safe", "non_ai", []string{"blue hair", "sketch"}, 1)
	seedSelectionArtwork(t, s, 713, 700, "unknown", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 714, 700, "safe", "unknown", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 715, 700, "safe", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 716, 701, "safe", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 717, 702, "safe", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	seedSelectionArtwork(t, s, 718, 703, "safe", "non_ai", []string{"blue hair", "night", "sketch"}, 1)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artwork_preferences(illust_id,value,updated_at) VALUES(715,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	putSelectionRules(t, s, domain.SelectionRules{
		AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"},
		IncludeTags: [][]string{{"blue hair", "azure"}, {"night"}},
		Orientation: "any", Strategy: "balanced",
	})

	got, err := s.Select(ctx, domain.SelectionRequest{SelectionRules: domain.SelectionRules{
		AllowedRatings: []string{"safe", "r18"},
		IncludeTags:    [][]string{{"sketch", "snow"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.IllustID != 710 {
		t.Fatalf("selected illustration %d, want only artwork 710 to satisfy global AND request rules", got.IllustID)
	}
	if got.SelectionID == "" || got.OriginalURL == "" || got.Repeated {
		t.Fatalf("unexpected selected page: %+v", got)
	}

	_, err = s.Select(ctx, domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"r18"}}})
	if !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("request widening error = %v, want ErrNoMatch from empty global intersection", err)
	}
}

func TestSelectUnknownAndDislikeNeverPassDefaultRules(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 800, "approved")
	seedSelectionArtwork(t, s, 801, 800, "r18", "ai", nil, 1)
	seedSelectionArtwork(t, s, 802, 800, "unknown", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 803, 800, "safe", "unknown", nil, 1)
	seedSelectionArtwork(t, s, 804, 800, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 805, 800, "safe", "non_ai", nil, 1)

	got, err := s.Select(ctx, domain.SelectionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.IllustID != 804 && got.IllustID != 805 {
		t.Fatalf("default rules selected illust %d, want safe/non_ai art (and not r18/ai, unknown, or unknown AI)", got.IllustID)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artwork_preferences(illust_id,value,updated_at) VALUES(804,'dislike',1),(805,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("unknown and disliked-only remainder error = %v, want ErrNoMatch", err)
	}
}

func TestSelectRejectsStoredEmptyAllowLists(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 850, "approved")
	seedSelectionArtwork(t, s, 851, 850, "safe", "non_ai", nil, 1)
	putSelectionRules(t, s, domain.SelectionRules{AllowedRatings: []string{}, AllowedAI: []string{"ai"}})
	if _, err := s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty stored allowlist error = %v, want ErrInvalid", err)
	}
}

func TestSelectCatalogEmptyAndNoMatch(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrCatalogEmpty) {
		t.Fatalf("empty catalog error = %v, want ErrCatalogEmpty", err)
	}
	seedEligibleArtist(t, s, 900, "approved")
	seedSelectionArtwork(t, s, 901, 900, "unknown", "unknown", nil, 1)
	if _, err := s.Select(ctx, domain.SelectionRequest{}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("non-empty but unqualified catalog error = %v, want ErrNoMatch", err)
	}
}

func TestSelectAccountScopeIntersectsTagsAndSurvivesRepeatFallback(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1250, "approved")
	seedEligibleArtist(t, s, 1251, "approved")
	seedEligibleArtist(t, s, 1252, "approved")
	seedSelectionArtwork(t, s, 1260, 1250, "safe", "non_ai", []string{"in-scope"}, 1)
	seedSelectionArtwork(t, s, 1261, 1250, "safe", "non_ai", []string{"in-scope"}, 1)
	seedSelectionArtwork(t, s, 1262, 1251, "safe", "non_ai", []string{"needle"}, 1)
	seedSelectionArtwork(t, s, 1263, 1252, "safe", "non_ai", []string{"needle"}, 1)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts(id,name,token_file,created_at,updated_at,deleted) VALUES
		('one','One','/unused',1,1,0),('two','Two','/unused',1,1,0),('deleted','Deleted','/unused',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	// The account scope is membership based; followed=0 still represents a linked artist.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_artists(account_id,artist_id,followed) VALUES
		('one',1250,1),('one',1251,0),('two',1252,1),('deleted',1251,1)`); err != nil {
		t.Fatal(err)
	}

	intersection, err := s.Select(ctx, domain.SelectionRequest{AccountID: "one", SelectionRules: domain.SelectionRules{IncludeTags: [][]string{{"needle"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if intersection.IllustID != 1262 {
		t.Fatalf("account and tag intersection selected %d, want linked needle work 1262", intersection.IllustID)
	}
	if _, err = s.Select(ctx, domain.SelectionRequest{AccountID: "deleted"}); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("deleted account selection error = %v, want ErrNoMatch", err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM selection_history`); err != nil {
		t.Fatal(err)
	}

	seen := map[int64]bool{}
	fallbackSeen := false
	for i := 0; i < 10; i++ {
		got, selectErr := s.Select(ctx, domain.SelectionRequest{AccountID: "one", SelectionRules: domain.SelectionRules{Strategy: "random"}})
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		if got.IllustID != 1260 && got.IllustID != 1261 && got.IllustID != 1262 {
			t.Fatalf("account selection escaped scope: %+v", got)
		}
		if got.Repeated {
			fallbackSeen = true
		} else {
			seen[got.IllustID] = true
		}
	}
	if len(seen) != 3 || !fallbackSeen {
		t.Fatalf("expected all scoped works then scoped repeat fallback; distinct=%v fallback=%v", seen, fallbackSeen)
	}
}

func TestSelectConcurrentDedupFallbackAndBoundedHistory(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1000, "approved")
	for i := int64(0); i < 3; i++ {
		seedSelectionArtwork(t, s, 1010+i, 1000, "safe", "non_ai", nil, 1)
	}

	const requestCount = 24
	results := make([]domain.SelectedPage, requestCount)
	errs := make([]error, requestCount)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.Select(ctx, domain.SelectionRequest{})
		}(i)
	}
	wg.Wait()
	seenFresh := map[int64]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("selection %d failed: %v", i, err)
		}
		if results[i].Repeated {
			continue
		}
		if seenFresh[results[i].IllustID] {
			t.Fatalf("fresh selection repeated artwork %d", results[i].IllustID)
		}
		seenFresh[results[i].IllustID] = true
	}
	if len(seenFresh) != 3 {
		t.Fatalf("freshly selected %d distinct works, want all 3 before fallback", len(seenFresh))
	}
	for _, result := range results {
		if result.Repeated && result.IllustID != 1010 && result.IllustID != 1011 && result.IllustID != 1012 {
			t.Fatalf("fallback returned unexpected work: %+v", result)
		}
	}
	var historyCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM selection_history WHERE collection='preferred'`).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if historyCount != selectionRecentMax {
		t.Fatalf("selection history count = %d, want bounded %d", historyCount, selectionRecentMax)
	}
}

func TestBalancedSelectPrefersLikedPagesWithoutBypassingHardFilters(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1050, "approved")
	seedSelectionArtwork(t, s, 1051, 1050, "safe", "non_ai", nil, 1) // liked and eligible
	seedSelectionArtwork(t, s, 1052, 1050, "safe", "non_ai", nil, 1) // neutral and eligible
	seedSelectionArtwork(t, s, 1053, 1050, "r18", "non_ai", nil, 1)  // liked, excluded by content rules
	seedSelectionArtwork(t, s, 1054, 1050, "safe", "ai", nil, 1)     // liked, excluded by AI rules
	seedSelectionArtwork(t, s, 1055, 1050, "safe", "non_ai", nil, 1) // disliked, hard excluded
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artwork_preferences(illust_id,value,updated_at) VALUES
		(1051,'like',1),(1053,'like',1),(1054,'like',1),(1055,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	request := domain.SelectionRequest{ArtistID: 1050}

	first, err := s.Select(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.IllustID != 1051 || first.Repeated {
		t.Fatalf("balanced selection should prefer the eligible liked work: %+v", first)
	}

	second, err := s.Select(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.IllustID != 1052 || second.Repeated {
		t.Fatalf("recent dedup/content/dislike filters must outrank likes: %+v", second)
	}

	fallback, err := s.Select(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.Repeated || (fallback.IllustID != 1051 && fallback.IllustID != 1052) {
		t.Fatalf("repeat fallback should only relax recency, got %+v", fallback)
	}
}

func TestEligiblePageUsesFixedIdentityAndAdminPreviewHonorsVisibilityAndBlocks(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1100, "approved")
	seedSelectionArtwork(t, s, 1101, 1100, "r18g", "ai", nil, 2)
	putSelectionRules(t, s, domain.SelectionRules{AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"}, Orientation: "any"})

	first, err := s.EligiblePage(ctx, 1101, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EligiblePage(ctx, 1101, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.IllustID != 1101 || first.PageIndex != 1 || first.PreviewURL == "" || first.OriginalURL != "" || !first.PreviewIsThumbnail || second.PageIndex != first.PageIndex || second.PreviewURL != first.PreviewURL {
		t.Fatalf("preview did not preserve fixed page identity or hide original URL: first=%+v second=%+v", first, second)
	}
	if _, err := s.EligiblePage(ctx, 1101, 1, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("r18g/ai public supply error = %v, want ErrNotFound", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='blocked' WHERE id=1100`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligiblePage(ctx, 1101, 1, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("blocked thumbnail preview error = %v, want ErrNotFound", err)
	}
	if _, err := s.EligibleAdminPage(ctx, 1101, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("blocked original preview error = %v, want ErrNotFound", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET review_status='approved' WHERE id=1100`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET visible=0 WHERE id=1101`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EligiblePage(ctx, 1101, 1, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden thumbnail preview error = %v, want ErrNotFound", err)
	}
	if _, err := s.EligibleAdminPage(ctx, 1101, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden original preview error = %v, want ErrNotFound", err)
	}
}

func seedEligibleArtist(t *testing.T, s *Store, id int64, status string) {
	t.Helper()
	seedArtist(t, s, id)
	if _, err := s.db.ExecContext(context.Background(), `UPDATE artists SET review_status=? WHERE id=?`, status, id); err != nil {
		t.Fatal(err)
	}
}

func seedSelectionArtwork(t *testing.T, s *Store, id, artistID int64, rating, ai string, tags []string, pageCount int) {
	t.Helper()
	pages := make([]domain.Page, pageCount)
	for i := range pages {
		pages[i] = domain.Page{
			Index: i, OriginalURL: fmt.Sprintf("https://i.pximg.net/illust/%d_p%d.jpg", id, i),
			PreviewURL: fmt.Sprintf("https://i.pximg.net/preview/%d_p%d.jpg", id, i),
			Width:      1200, Height: 800,
		}
	}
	if err := s.UpsertArtwork(context.Background(), domain.Artwork{
		ID: id, ArtistID: artistID, Kind: "illust", Rating: rating, AIStatus: ai,
		MetadataState: "complete", Visible: true, PageCount: pageCount, Tags: tags, Pages: pages,
	}); err != nil {
		t.Fatal(err)
	}
}

func putSelectionRules(t *testing.T, s *Store, rules domain.SelectionRules) {
	t.Helper()
	encoded, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(context.Background(), "preferences", encoded, 0); err != nil {
		t.Fatal(err)
	}
}

func TestSelectUnknownRequiresExplicitGlobalPermissionAndKeepsHardExclusions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1600, "approved")
	seedEligibleArtist(t, s, 1601, "blocked")
	seedSelectionArtwork(t, s, 1610, 1600, "unknown", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 1611, 1600, "safe", "unknown", nil, 1)
	seedSelectionArtwork(t, s, 1612, 1600, "unknown", "unknown", nil, 1)
	seedSelectionArtwork(t, s, 1613, 1601, "unknown", "unknown", nil, 1)
	request := domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"unknown"}, AllowedAI: []string{"unknown"}}}
	if _, err := s.Select(ctx, request); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("default rules widened to unknown: %v", err)
	}
	putSelectionRules(t, s, domain.SelectionRules{AllowedRatings: []string{"safe", "r18", "r18g", "unknown"}, AllowedAI: []string{"non_ai", "ai", "unknown"}})
	got, err := s.Select(ctx, request)
	if err != nil || got.IllustID != 1612 {
		t.Fatalf("explicit unknown pick=%+v err=%v", got, err)
	}
	if _, err = s.EligiblePage(ctx, 1612, 0, false); err != nil {
		t.Fatalf("explicit unknown fixed page: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO artwork_preferences(illust_id,value,updated_at) VALUES(1612,'dislike',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Select(ctx, request); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("unknown bypassed dislike/block: %v", err)
	}
	if _, err = s.EligiblePage(ctx, 1612, 0, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old URL bypassed dislike: %v", err)
	}
}

func TestExcludeMangaIsOptInAndGlobalPreferenceCannotBeRelaxed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1800, "approved")
	seedEligibleArtist(t, s, 1801, "approved")
	seedSelectionArtwork(t, s, 1810, 1800, "safe", "non_ai", nil, 1)
	seedSelectionArtwork(t, s, 1811, 1801, "safe", "non_ai", nil, 2)
	if _, err := s.db.ExecContext(ctx, `UPDATE artworks SET kind='manga' WHERE id=1810`); err != nil {
		t.Fatal(err)
	}
	global := domain.DefaultRules()
	putSelectionRules(t, s, global)
	base := domain.SelectionRequest{SelectionRules: domain.SelectionRules{AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"}, Orientation: "any"}, ArtistID: 1800}
	if got, err := s.Select(ctx, base); err != nil || got.IllustID != 1810 {
		t.Fatalf("default false must preserve manga: selected=%+v err=%v", got, err)
	}

	base.ExcludeManga = true
	if _, err := s.Select(ctx, base); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("request exclusion did not exclude manga: %v", err)
	}
	base.ExcludeManga = false
	global.ExcludeManga = true
	encoded, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx, "preferences", encoded, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Select(ctx, base); !errors.Is(err, domain.ErrNoMatch) {
		t.Fatalf("request false relaxed global manga exclusion: %v", err)
	}
	base.ArtistID = 1801
	got, err := s.Select(ctx, base)
	if err != nil || got.IllustID != 1811 {
		t.Fatalf("non-manga multi-page candidate was excluded: selected=%+v err=%v", got, err)
	}
}

func TestUnknownPageDimensionsOnlyMatchUnrestrictedOrientation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	seedEligibleArtist(t, s, 1820, "approved")
	seedSelectionArtwork(t, s, 1821, 1820, "safe", "non_ai", nil, 2)
	if _, err := s.db.ExecContext(ctx, `UPDATE image_pages SET width=0,height=0 WHERE illust_id=1821 AND page_index=1`); err != nil {
		t.Fatal(err)
	}
	pageMatches := func(rules domain.SelectionRules) int {
		t.Helper()
		where, args, err := selectionWhere(rules, 0, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		args = append(args, int64(1821), 1)
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where+` AND a.id=? AND p.page_index=?`, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	base := domain.DefaultRules()
	if got := pageMatches(base); got != 1 {
		t.Fatalf("orientation=any excluded unknown-size fixed page: %d", got)
	}
	for _, orientation := range []string{"portrait", "landscape", "square"} {
		rules := base
		rules.Orientation = orientation
		if got := pageMatches(rules); got != 0 {
			t.Errorf("orientation=%s matched unknown-size page: %d", orientation, got)
		}
	}
	for _, rule := range []domain.SelectionRules{
		{MinWidth: 1},
		{MinHeight: 1},
	} {
		rules := base
		rules.MinWidth, rules.MinHeight = rule.MinWidth, rule.MinHeight
		if got := pageMatches(rules); got != 0 {
			t.Errorf("minimum size matched unknown dimension: rules=%+v count=%d", rules, got)
		}
	}
	if _, err := s.EligiblePage(ctx, 1821, 1, false); err != nil {
		t.Fatalf("fixed page must remain eligible under orientation=any: %v", err)
	}
}

func TestPageDimensionsMigrationClearsUnverifiedSecondaryPages(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dimensions.db")
	version, _ := findMigrationContaining(t, "UPDATE image_pages SET width=0,height=0")
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < version-1; i++ {
		body, readErr := migrations.ReadFile("migrations/" + entries[i].Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply pre-dimension migration %s: %v", entries[i].Name(), err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, i+1, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO artists(id,name,review_status,created_at,updated_at) VALUES(1830,'dimensions artist','approved',1,1)`,
		`INSERT INTO artworks(id,artist_id,title,kind,page_count,rating,ai_status,metadata_state,created_at,updated_at) VALUES(1831,1830,'dimensions','illust',2,'safe','non_ai','complete',1,1)`,
		`INSERT INTO image_pages(illust_id,page_index,original_url,width,height,media_state,created_at,updated_at) VALUES(1831,0,'https://i.pximg.net/img-original/1831_p0.jpg',1600,900,'indexed',1,1),(1831,1,'https://i.pximg.net/img-original/1831_p1.jpg',1600,900,'indexed',1,1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	artwork := domain.Artwork{
		ID: 1831, ArtistID: 1830, Kind: "illust", PageCount: 2, Rating: "safe", AIStatus: "non_ai", MetadataState: "complete",
		Pages: []domain.Page{
			{Index: 0, OriginalURL: "https://i.pximg.net/img-original/1831_p0.jpg", Width: 1600, Height: 900, State: "indexed"},
			{Index: 1, OriginalURL: "https://i.pximg.net/img-original/1831_p1.jpg", Width: 1600, Height: 900, State: "indexed"},
		},
	}
	var firstWidth, firstHeight, nextWidth, nextHeight int
	if err := upgraded.db.QueryRowContext(ctx, `SELECT width,height FROM image_pages WHERE illust_id=1831 AND page_index=0`).Scan(&firstWidth, &firstHeight); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRowContext(ctx, `SELECT width,height FROM image_pages WHERE illust_id=1831 AND page_index=1`).Scan(&nextWidth, &nextHeight); err != nil {
		t.Fatal(err)
	}
	if firstWidth != 1600 || firstHeight != 900 || nextWidth != 0 || nextHeight != 0 {
		t.Fatalf("migration dimensions: first=%dx%d second=%dx%d", firstWidth, firstHeight, nextWidth, nextHeight)
	}
	artwork.Pages[1].Width, artwork.Pages[1].Height = 0, 0
	if err := upgraded.UpsertArtwork(ctx, artwork); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRowContext(ctx, `SELECT width,height FROM image_pages WHERE illust_id=1831 AND page_index=1`).Scan(&nextWidth, &nextHeight); err != nil {
		t.Fatal(err)
	}
	if nextWidth != 0 || nextHeight != 0 {
		t.Fatalf("upsert restored unverified first-page dimensions to later page: %dx%d", nextWidth, nextHeight)
	}
}
