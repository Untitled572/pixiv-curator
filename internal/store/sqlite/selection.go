package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	var artworkCount int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artworks`).Scan(&artworkCount); err != nil {
		return domain.SelectedPage{}, err
	}
	if artworkCount == 0 {
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
		selected, err = choosePage(ctx, tx, rules, req.ArtistID, req.AccountID, recentIDs, false)
		if err != nil {
			return domain.SelectedPage{}, err
		}
		if selected.page.IllustID == 0 {
			selected, err = choosePage(ctx, tx, rules, req.ArtistID, req.AccountID, nil, true)
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO distribution_history(id,collection,illust_id,page_index,selected_at,request_id) VALUES(?,?,?,?,?,?)`, selectionID, selectionCollection, selected.page.IllustID, selected.page.PageIndex, now, selectionID); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT illust_id FROM selection_history WHERE collection=? AND selected_at>=? GROUP BY illust_id ORDER BY MAX(selected_at) DESC LIMIT ?`, selectionCollection, since, selectionRecentMax)
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

func choosePage(ctx context.Context, tx *sql.Tx, rules domain.SelectionRules, artistID int64, accountID string, recentIDs []int64, fallback bool) (selectionPage, error) {
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
	if rules.Strategy == "random" {
		rows, err := selectionRows(ctx, tx, where, args, `random()`, 1)
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
	for _, id := range artistIDs {
		artistWhere := appendAnd(where, `a.artist_id=?`)
		artistArgs := append(append([]any(nil), args...), id)
		rows, err := selectionRows(ctx, tx, artistWhere, artistArgs, `CASE WHEN EXISTS (SELECT 1 FROM artwork_preferences pref WHERE pref.illust_id=a.id AND pref.value='like') THEN 0 ELSE 1 END, random()`, 1)
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
	where := `ar.review_status='approved' AND a.visible=1 AND a.metadata_state='complete' AND a.kind IN ('illust','manga') AND p.media_state='indexed' AND a.rating IN ('safe','r18','r18g','unknown') AND a.ai_status IN ('non_ai','ai','unknown') AND NOT EXISTS (SELECT 1 FROM artwork_preferences pref WHERE pref.illust_id=a.id AND pref.value='dislike')`
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
	switch normalizedOrientation(rules.Orientation) {
	case "portrait":
		where = appendAnd(where, `p.width>0 AND p.height>0 AND p.height*10>p.width*11`)
	case "landscape":
		where = appendAnd(where, `p.width>0 AND p.height>0 AND p.width*10>p.height*11`)
	case "square":
		where = appendAnd(where, `p.width>0 AND p.height>0 AND p.width*10<=p.height*11 AND p.height*10<=p.width*11`)
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
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.artist_id FROM artists ar JOIN artworks a ON a.artist_id=ar.id JOIN image_pages p ON p.illust_id=a.id WHERE `+where+` ORDER BY random() LIMIT 20`, args...)
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

func selectionRows(ctx context.Context, tx *sql.Tx, where string, args []any, orderBy string, limit int) ([]selectionPage, error) {
	query := selectionPageQuery(where, orderBy) + fmt.Sprintf(" LIMIT %d", limit)
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

func selectionPageQuery(where, orderBy string) string {
	query := `SELECT a.id,a.artist_id,p.page_index,p.width,p.height,p.original_url,p.preview_url
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
