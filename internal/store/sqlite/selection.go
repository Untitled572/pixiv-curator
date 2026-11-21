package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	randv2 "math/rand/v2"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const (
	selectionCollection = "preferred"
	selectionWindow     = 30 * time.Minute
	selectionRecentMax  = 20
)

type selectionPage struct {
	page domain.SelectedPage
}

// Select chooses a qualified image page, writes both the bounded anti-repeat
// record and the persistent distribution event in one transaction.
func (s *Store) Select(ctx context.Context, req domain.SelectionRequest) (domain.SelectedPage, error) {
	if req.ArtistID < 0 {
		return domain.SelectedPage{}, domain.ErrInvalid
	}
	if err := domain.ValidateRules(req.SelectionRules); err != nil {
		return domain.SelectedPage{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	defer tx.Rollback()

	var hasArtworks bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artworks)`).Scan(&hasArtworks); err != nil {
		return domain.SelectedPage{}, err
	}
	if !hasArtworks {
		return domain.SelectedPage{}, domain.ErrCatalogEmpty
	}

	global, err := preferencesInTx(ctx, tx)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	rules, impossible, err := combineSelectionRules(global, req)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	selectionID, err := newSelectionID()
	if err != nil {
		return domain.SelectedPage{}, err
	}

	var selected selectionPage
	repeated := false
	if !impossible {
		recentIDs, e := recentArtworkIDs(ctx, tx, time.Now().Add(-selectionWindow).UnixMilli())
		if e != nil {
			return domain.SelectedPage{}, e
		}
		selected, err = s.choosePage(ctx, tx, rules, req.ArtistID, req.AccountID, recentIDs, false)
		if err != nil {
			return domain.SelectedPage{}, err
		}
		if selected.page.IllustID == 0 {
			selected, err = s.choosePage(ctx, tx, rules, req.ArtistID, req.AccountID, nil, true)
			if err != nil {
				return domain.SelectedPage{}, err
			}
			repeated = selected.page.IllustID != 0
		}
	}
	if selected.page.IllustID == 0 {
		return domain.SelectedPage{}, domain.ErrNoMatch
	}

	now := time.Now().UnixMilli()
	selected.page.SelectionID = selectionID
	selected.page.Repeated = repeated
	if _, err = tx.ExecContext(ctx, `INSERT INTO selection_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,?,?,?,?,?)`, selectionID, selectionCollection, selected.page.IllustID, selected.page.PageIndex, now, selectionID); err != nil {
		return domain.SelectedPage{}, err
	}
	if err = insertDistributionSelection(ctx, tx, selected.page, rules, req, now, selectionID); err != nil {
		return domain.SelectedPage{}, err
	}
	if err = trimDistributionHistory(ctx, tx, time.UnixMilli(now)); err != nil {
		return domain.SelectedPage{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM selection_history WHERE collection=? AND selected_at<?`, selectionCollection, now-selectionWindow.Milliseconds()); err != nil {
		return domain.SelectedPage{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM selection_history WHERE collection=? AND id NOT IN (SELECT id FROM selection_history WHERE collection=? ORDER BY selected_at DESC,id DESC LIMIT ?)`, selectionCollection, selectionCollection, selectionRecentMax); err != nil {
		return domain.SelectedPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.SelectedPage{}, err
	}
	return selected.page, nil
}

// EligiblePage resolves a fixed artwork/page identity. Preview mode is intended
// only for a separately authenticated management route and omits the original URL.
func (s *Store) EligiblePage(ctx context.Context, id int64, index int, preview bool) (domain.SelectedPage, error) {
	if id <= 0 || index < 0 {
		return domain.SelectedPage{}, domain.ErrInvalid
	}
	if preview {
		result, err := s.EligibleAdminPage(ctx, id, index)
		result.OriginalURL = ""
		return result, err
	}

	return s.EligiblePageWithRequest(ctx, id, index, domain.SelectionRequest{})
}

// EligiblePageWithRequest resolves one fixed identity with explicit API options.
func (s *Store) EligiblePageWithRequest(ctx context.Context, id int64, index int, req domain.SelectionRequest) (domain.SelectedPage, error) {
	if id <= 0 || index < 0 {
		return domain.SelectedPage{}, domain.ErrInvalid
	}
	global, err := preferences(ctx, s.db)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	rules, impossible, err := combineSelectionRules(global, req)
	if impossible {
		return domain.SelectedPage{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SelectedPage{}, err
	}
	if err := domain.ValidateRules(rules); err != nil {
		return domain.SelectedPage{}, err
	}
	where, args, err := selectionWhere(rules, req.ArtistID, req.AccountID, nil)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	where = appendAnd(where, `a.id=? AND p.page_index=?`)
	args = append(args, id, index)
	query := selectionPageQuery(where, "")
	var result domain.SelectedPage
	if err = s.db.QueryRowContext(ctx, query, args...).Scan(&result.IllustID, &result.ArtistID, &result.PageIndex, &result.Width, &result.Height, &result.OriginalURL, &result.PreviewURL); err != nil {
		if err == sql.ErrNoRows {
			return domain.SelectedPage{}, domain.ErrNotFound
		}
		return domain.SelectedPage{}, err
	}
	if !domain.ValidImageURL(result.OriginalURL) {
		return domain.SelectedPage{}, domain.ErrNotFound
	}
	if result.PreviewURL != "" && !domain.ValidImageURL(result.PreviewURL) {
		result.PreviewURL = ""
	}
	return result, nil
}

// EligibleAdminPage resolves a fixed visible image page for management preview.
// It deliberately ignores public rating and AI preferences, but never serves
// hidden artwork or artwork from a blocked artist.
func (s *Store) EligibleAdminPage(ctx context.Context, id int64, index int) (domain.SelectedPage, error) {
	if id <= 0 || index < 0 {
		return domain.SelectedPage{}, domain.ErrInvalid
	}
	var result domain.SelectedPage
	var previewURL string
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.artist_id,p.page_index,p.width,p.height,p.original_url,p.preview_url
		FROM artworks a JOIN artists ar ON ar.id=a.artist_id JOIN image_pages p ON p.illust_id=a.id
		WHERE a.id=? AND p.page_index=? AND p.media_state='indexed' AND a.visible=1 AND ar.review_status<>'blocked'`, id, index).
		Scan(&result.IllustID, &result.ArtistID, &result.PageIndex, &result.Width, &result.Height, &result.OriginalURL, &previewURL)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.SelectedPage{}, domain.ErrNotFound
		}
		return domain.SelectedPage{}, err
	}
	if previewURL == "" {
		previewURL = result.OriginalURL
	}
	if !domain.ValidImageURL(result.OriginalURL) || !domain.ValidImageURL(previewURL) {
		return domain.SelectedPage{}, domain.ErrNotFound
	}
	result.PreviewURL = previewURL
	result.PreviewIsThumbnail = previewURL != result.OriginalURL
	return result, nil
}

func preferences(ctx context.Context, db *sql.DB) (domain.SelectionRules, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key='preferences'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return domain.DefaultRules(), nil
	}
	if err != nil {
		return domain.SelectionRules{}, err
	}
	rules := domain.DefaultRules()
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return domain.SelectionRules{}, fmt.Errorf("decode preferences: %w", domain.ErrInvalid)
	}
	if err := validateStoredRules(rules); err != nil {
		return domain.SelectionRules{}, err
	}
	if rules.Orientation == "" {
		rules.Orientation = "any"
	}
	if rules.Strategy == "" {
		rules.Strategy = "balanced"
	}
	return rules, nil
}

func preferencesInTx(ctx context.Context, tx *sql.Tx) (domain.SelectionRules, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key='preferences'`).Scan(&raw)
	if err == sql.ErrNoRows {
		return domain.DefaultRules(), nil
	}
	if err != nil {
		return domain.SelectionRules{}, err
	}
	var rules domain.SelectionRules
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return domain.SelectionRules{}, fmt.Errorf("decode preferences: %w", domain.ErrInvalid)
	}
	if err := validateStoredRules(rules); err != nil {
		return domain.SelectionRules{}, err
	}
	if rules.Orientation == "" {
		rules.Orientation = "any"
	}
	if rules.Strategy == "" {
		rules.Strategy = "balanced"
	}
	return rules, nil
}

func validateStoredRules(rules domain.SelectionRules) error {
	if len(rules.AllowedRatings) == 0 || len(rules.AllowedAI) == 0 {
		return domain.ErrInvalid
	}
	return domain.ValidateRules(rules)
}

func combineSelectionRules(global domain.SelectionRules, req domain.SelectionRequest) (domain.SelectionRules, bool, error) {
	if err := domain.ValidateRules(global); err != nil {
		return domain.SelectionRules{}, false, err
	}
	if err := domain.ValidateRules(req.SelectionRules); err != nil {
		return domain.SelectionRules{}, false, err
	}
	result := global
	result.OrientationFallback = req.OrientationFallback
	result.DeliveryMode = req.DeliveryMode
	result.ExcludeManga = global.ExcludeManga || req.ExcludeManga
	result.AllowedRatings = intersectStrings(global.AllowedRatings, req.AllowedRatings)
	result.AllowedAI = intersectStrings(global.AllowedAI, req.AllowedAI)
	result.IncludeTags = append(cloneGroups(global.IncludeTags), cloneGroups(req.IncludeTags)...)
	result.ExcludeTags = uniqueStrings(append(append([]string(nil), global.ExcludeTags...), req.ExcludeTags...))
	if req.MinWidth > result.MinWidth {
		result.MinWidth = req.MinWidth
	}
	if req.MinHeight > result.MinHeight {
		result.MinHeight = req.MinHeight
	}
	globalOrientation := normalizedOrientation(global.Orientation)
	requestOrientation := normalizedOrientation(req.Orientation)
	impossible := globalOrientation != "any" && requestOrientation != "any" && globalOrientation != requestOrientation
	if globalOrientation == "any" {
		result.Orientation = requestOrientation
	} else {
		result.Orientation = globalOrientation
	}
	if req.Strategy != "" {
		result.Strategy = req.Strategy
	}
	if result.Strategy == "" {
		result.Strategy = "balanced"
	}
	if result.Orientation == "" {
		result.Orientation = "any"
	}
	// Explicit API options replace defaults for that field. Blocked artists and
	// disliked artworks remain unconditional eligibility constraints.
	if req.Overrides["r18"] {
		result.AllowedRatings = apiAllowedCategories(global.AllowedRatings, req.AllowedRatings)
	}
	if req.Overrides["ai_type"] {
		result.AllowedAI = apiAllowedCategories(global.AllowedAI, req.AllowedAI)
	}
	if req.Overrides["exclude_manga"] {
		result.ExcludeManga = req.ExcludeManga
	}
	if req.Overrides["orientation"] {
		result.Orientation = requestOrientation
		impossible = false
	}
	if req.Overrides["min_width"] {
		result.MinWidth = req.MinWidth
	}
	if req.Overrides["min_height"] {
		result.MinHeight = req.MinHeight
	}
	if req.Overrides["included_tags"] {
		result.IncludeTags = cloneGroups(req.IncludeTags)
	}
	if req.Overrides["excluded_tags"] {
		result.ExcludeTags = uniqueStrings(req.ExcludeTags)
	}
	return result, impossible, nil
}

// Known categories may override defaults; unknown still needs explicit global permission.
func apiAllowedCategories(global, request []string) []string {
	allowUnknown := false
	for _, value := range global {
		if value == "unknown" {
			allowUnknown = true
		}
	}
	result := make([]string, 0, len(request))
	for _, value := range request {
		if value != "unknown" || allowUnknown {
			result = append(result, value)
		}
	}
	return result
}

func intersectStrings(global, request []string) []string {
	if global == nil {
		if request == nil {
			return nil
		}
		result := make([]string, len(request))
		copy(result, request)
		return result
	}
	if request == nil {
		return append([]string{}, global...)
	}
	allowed := make(map[string]struct{}, len(request))
	for _, value := range request {
		allowed[value] = struct{}{}
	}
	result := make([]string, 0, len(global))
	for _, value := range global {
		if _, ok := allowed[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

func cloneGroups(groups [][]string) [][]string {
	result := make([][]string, len(groups))
	for i, group := range groups {
		result[i] = append([]string(nil), group...)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeTag(value)
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func normalizedOrientation(value string) string {
	if value == "" {
		return "any"
	}
	return value
}

func recentArtworkIDs(ctx context.Context, tx *sql.Tx, since int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT illust_id FROM (SELECT illust_id FROM selection_history WHERE collection=? AND selected_at>=? ORDER BY selected_at DESC,id DESC LIMIT ?)`, selectionCollection, since, selectionRecentMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) choosePage(ctx context.Context, tx *sql.Tx, rules domain.SelectionRules, artistID int64, accountID string, recentIDs []int64, fallback bool) (selectionPage, error) {
	where, args, err := selectionWhere(rules, artistID, accountID, recentIDs)
	if err != nil {
		return selectionPage{}, err
	}
	if fallback {
		rows, err := selectionRows(ctx, tx, where, args, `COALESCE((SELECT MAX(h.selected_at) FROM selection_history h WHERE h.collection='preferred' AND h.illust_id=a.id),0) ASC, random()`, selectionRecentMax)
		if err != nil {
			return selectionPage{}, err
		}
		for _, row := range rows {
			if domain.ValidImageURL(row.page.OriginalURL) {
				return row, nil
			}
		}
		return selectionPage{}, nil
	}
	if rules.Strategy == "stratified" || rules.Strategy == "popular" {
		return s.chooseStratified(ctx, tx, rules, where, args, artistID, accountID, recentIDs)
	}
	if rules.Strategy == "random" {
		counts, err := imageFeedbackInTx(ctx, tx)
		if err != nil {
			return selectionPage{}, err
		}
		limit := 1
		if len(counts) > 0 {
			limit = 64
		}
		var rows []selectionPage
		if artistID == 0 {
			rows, err = sampleSelectionRows(ctx, tx, where, args, limit)
			if err != nil {
				return selectionPage{}, err
			}
		}
		if len(rows) < limit {
			rows, err = selectionRows(ctx, tx, where, args, `random()`, limit)
		}
		if err != nil {
			return selectionPage{}, err
		}
		rows, err = feedbackOrderPages(ctx, tx, rows, counts, 1)
		if err != nil {
			return selectionPage{}, err
		}
		for _, row := range rows {
			if domain.ValidImageURL(row.page.OriginalURL) {
				return row, nil
			}
		}
		return selectionPage{}, nil
	}
	artistIDs, err := selectionArtistIDs(ctx, tx, where, args)
	if err != nil {
		return selectionPage{}, err
	}
	counts, err := imageFeedbackInTx(ctx, tx)
	if err != nil {
		return selectionPage{}, err
	}
	artistIDs, err = feedbackOrderArtists(ctx, tx, artistIDs, counts)
	if err != nil {
		return selectionPage{}, err
	}
	for _, id := range artistIDs {
		artistWhere := appendAnd(where, `a.artist_id=?`)
		artistArgs := append(append([]any(nil), args...), id)
		limit := 1
		if len(counts) > 0 {
			limit = 20
		}
		rows, err := selectionRows(ctx, tx, artistWhere, artistArgs, `CASE WHEN EXISTS (SELECT 1 FROM artwork_preferences pref WHERE pref.illust_id=a.id AND pref.value='like') THEN 0 ELSE 1 END, random()`, limit)
		if err != nil {
			return selectionPage{}, err
		}
		rows, err = feedbackOrderPages(ctx, tx, rows, counts, .5)
		if err != nil {
			return selectionPage{}, err
		}
		for _, row := range rows {
			if domain.ValidImageURL(row.page.OriginalURL) {
				return row, nil
			}
		}
	}
	return selectionPage{}, nil
}

func selectionWhere(rules domain.SelectionRules, artistID int64, accountID string, recentIDs []int64) (string, []any, error) {
	where := `ar.review_status='approved' AND ar.collection_enabled=1 AND a.visible=1 AND a.metadata_state='complete' AND a.kind IN ('illust','manga') AND p.media_state='indexed' AND a.rating IN ('safe','r18','r18g','unknown') AND a.ai_status IN ('non_ai','ai','unknown') AND NOT EXISTS (SELECT 1 FROM artwork_preferences pref WHERE pref.illust_id=a.id AND pref.value='dislike')`
	where = appendAnd(where, `NOT EXISTS(SELECT 1 FROM image_feedback f WHERE f.illust_id=a.id AND f.page_index=p.page_index AND f.value='dislike')`)
	var args []any
	if artistID > 0 {
		where = appendAnd(where, `a.artist_id=?`)
		args = append(args, artistID)
	}
	if rules.ExcludeManga {
		where = appendAnd(where, `a.kind<>'manga'`)
	}
	if accountID != "" {
		where = appendAnd(where, `EXISTS (SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.account_id=? AND aa.artist_id=a.artist_id AND ac.deleted=0)`)
		args = append(args, accountID)
	}
	if len(recentIDs) > 0 {
		where = appendAnd(where, `a.id NOT IN (`+placeholders(len(recentIDs))+`)`)
		for _, id := range recentIDs {
			args = append(args, id)
		}
	}
	if rules.AllowedRatings != nil {
		where = appendAnd(where, `a.rating IN (`+placeholders(len(rules.AllowedRatings))+`)`)
		for _, value := range rules.AllowedRatings {
			args = append(args, value)
		}
	}
	if rules.AllowedAI != nil {
		where = appendAnd(where, `a.ai_status IN (`+placeholders(len(rules.AllowedAI))+`)`)
		for _, value := range rules.AllowedAI {
			args = append(args, value)
		}
	}
	if rules.MinWidth > 0 {
		where = appendAnd(where, `p.width>=?`)
		args = append(args, rules.MinWidth)
	}
	if rules.MinHeight > 0 {
		where = appendAnd(where, `p.height>=?`)
		args = append(args, rules.MinHeight)
	}
	orientationWidth := "p.width"
	orientationHeight := "p.height"
	if rules.OrientationFallback == "first_page" {
		orientationWidth = "CASE WHEN (p.width=0 OR p.height=0) AND p.page_index>0 THEN COALESCE((SELECT p0.width FROM image_pages p0 WHERE p0.illust_id=p.illust_id AND p0.page_index=0),0) ELSE p.width END"
		orientationHeight = "CASE WHEN (p.width=0 OR p.height=0) AND p.page_index>0 THEN COALESCE((SELECT p0.height FROM image_pages p0 WHERE p0.illust_id=p.illust_id AND p0.page_index=0),0) ELSE p.height END"
	}
	switch normalizedOrientation(rules.Orientation) {
	case "portrait":
		where = appendAnd(where, orientationWidth+`>0 AND `+orientationHeight+`>0 AND (`+orientationHeight+`)*10>(`+orientationWidth+`)*11`)
	case "landscape":
		where = appendAnd(where, orientationWidth+`>0 AND `+orientationHeight+`>0 AND (`+orientationWidth+`)*10>(`+orientationHeight+`)*11`)
	case "square":
		where = appendAnd(where, orientationWidth+`>0 AND `+orientationHeight+`>0 AND (`+orientationWidth+`)*10<=(`+orientationHeight+`)*11 AND (`+orientationHeight+`)*10<=(`+orientationWidth+`)*11`)
	}
	for _, group := range rules.IncludeTags {
		where = appendAnd(where, `EXISTS (SELECT 1 FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id=a.id AND t.normalized_name IN (`+placeholders(len(group))+`))`)
		for _, tag := range group {
			args = append(args, normalizeTag(tag))
		}
	}
	if len(rules.ExcludeTags) > 0 {
		where = appendAnd(where, `NOT EXISTS (SELECT 1 FROM artwork_tags at JOIN tags t ON t.id=at.tag_id WHERE at.illust_id=a.id AND t.normalized_name IN (`+placeholders(len(rules.ExcludeTags))+`))`)
		for _, tag := range rules.ExcludeTags {
			args = append(args, normalizeTag(tag))
		}
	}
	return where, args, nil
}

func selectionArtistIDs(ctx context.Context, tx *sql.Tx, where string, args []any) ([]int64, error) {
	// Stop at the first eligible page for each artist. Use the artist index
	// because the global eligibility index would be scanned once per artist.
	rows, err := tx.QueryContext(ctx, `SELECT ar.id FROM artists ar
		WHERE ar.review_status='approved' AND ar.collection_enabled=1 AND EXISTS (
		SELECT 1 FROM artworks a INDEXED BY artworks_artist JOIN image_pages p ON p.illust_id=a.id
		WHERE a.artist_id=ar.id AND `+where+`) ORDER BY random() LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// sampleSelectionRows draws a uniform subset of row IDs. Deleted rows are
// ignored and every candidate is filtered against the current transaction.
// When fewer than limit pages qualify, the caller runs the exact query.
func sampleSelectionRows(ctx context.Context, tx *sql.Tx, where string, args []any, limit int) ([]selectionPage, error) {
	var first, last int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT MIN(rowid) FROM image_pages),0),
		COALESCE((SELECT MAX(rowid) FROM image_pages),0)`).Scan(&first, &last); err != nil {
		return nil, err
	}
	sampleSize := max(64, limit*16)
	if first <= 0 || last-first < int64(sampleSize) {
		return nil, nil
	}

	seen := make(map[int64]struct{}, sampleSize)
	params := make([]any, 0, sampleSize+len(args)+1)
	for len(seen) < sampleSize {
		id := first + randv2.Int64N(last-first+1)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		params = append(params, id)
	}
	params = append(params, args...)
	params = append(params, limit)
	query := `SELECT ` + selectionColumns + `
		FROM image_pages p CROSS JOIN artworks a ON a.id=p.illust_id
		CROSS JOIN artists ar ON ar.id=a.artist_id
		WHERE p.rowid IN (` + placeholders(sampleSize) + `) AND ` + where + ` ORDER BY random() LIMIT ?`
	return querySelectionRows(ctx, tx, query, params)
}

func selectionRows(ctx context.Context, tx *sql.Tx, where string, args []any, orderBy string, limit int) ([]selectionPage, error) {
	query := selectionPageQuery(where, orderBy) + fmt.Sprintf(" LIMIT %d", limit)
	return querySelectionRows(ctx, tx, query, args)
}

func querySelectionRows(ctx context.Context, tx *sql.Tx, query string, args []any) ([]selectionPage, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []selectionPage
	for rows.Next() {
		var row selectionPage
		if err = rows.Scan(&row.page.IllustID, &row.page.ArtistID, &row.page.PageIndex, &row.page.Width, &row.page.Height, &row.page.OriginalURL, &row.page.PreviewURL); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

const selectionColumns = `a.id,a.artist_id,p.page_index,p.width,p.height,p.original_url,p.preview_url`

func selectionPageQuery(where, orderBy string) string {
	query := `SELECT ` + selectionColumns + `
		FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE ` + where
	if orderBy != "" {
		query += ` ORDER BY ` + orderBy
	}
	return query
}

func appendAnd(where, clause string) string { return where + ` AND ` + clause }

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func normalizeTag(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func newSelectionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
