package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const artistTagsCTEs = `, recent AS (
 SELECT w.id,w.artist_id,ROW_NUMBER() OVER(PARTITION BY w.artist_id ORDER BY COALESCE(w.published_at,0) DESC,w.id DESC) AS rn
 FROM artworks w JOIN selected_artists a ON a.id=w.artist_id
), automatic_counts AS (
 SELECT r.artist_id,t.normalized_name,t.display_name,COUNT(*) AS n
 FROM recent r JOIN artwork_tags at ON at.illust_id=r.id JOIN tags t ON t.id=at.tag_id
 WHERE r.rn<=100 GROUP BY r.artist_id,t.normalized_name,t.display_name
), automatic_ranked AS (
 SELECT artist_id,normalized_name,display_name,ROW_NUMBER() OVER(PARTITION BY artist_id ORDER BY n DESC,normalized_name) AS tag_rank
 FROM automatic_counts
), effective_tags AS (
 SELECT o.artist_id,TRIM(j.value) AS display_name,CAST(j.key AS INTEGER) AS tag_order
 FROM artist_tag_overrides o JOIN selected_artists a ON a.id=o.artist_id,json_each(o.tags_json) j
 UNION ALL
 SELECT ar.artist_id,ar.display_name,ar.tag_rank AS tag_order FROM automatic_ranked ar
 WHERE ar.tag_rank<=8 AND NOT EXISTS(SELECT 1 FROM artist_tag_overrides o WHERE o.artist_id=ar.artist_id)
)`

const selectedArtistsCTE = `WITH selected_artists AS (
 SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed
 FROM artists a WHERE a.review_status IN ('approved','blocked')
 AND (?='' OR EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.account_id=? AND ac.deleted=0))
 AND a.id>?
)`

const pagedArtistsCTE = `WITH selected_artists AS (
 SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed
 FROM artists a WHERE a.review_status IN ('approved','blocked')
 AND (?='' OR EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.account_id=? AND ac.deleted=0))
 AND a.id>? ORDER BY a.id LIMIT ? OFFSET ?
)`

// ArtistTagPage returns the legacy keyset page, including one extra row when present.
func (s *Store) ArtistTagPage(ctx context.Context, tag, accountID string, after int64, limit int) ([]domain.Artist, error) {
	if after < 0 || limit < 1 || limit > 200 || strings.TrimSpace(tag) != tag || strings.TrimSpace(accountID) != accountID {
		return nil, domain.ErrInvalid
	}
	items, err := s.artistTagQuery(ctx, tag, accountID, after, 0, limit+1)
	return items, err
}

// ArtistTagPageStats returns a stable page plus filtered and full library counts.
func (s *Store) ArtistTagPageStats(ctx context.Context, tag, accountID string, page, pageSize int) ([]domain.Artist, int64, int64, int, string, error) {
	if page < 1 || pageSize < 1 || pageSize > 200 || strings.TrimSpace(tag) != tag || strings.TrimSpace(accountID) != accountID {
		return nil, 0, 0, 0, "", domain.ErrInvalid
	}
	offset := (page - 1) * pageSize
	if offset < 0 || offset/pageSize != page-1 || offset > int(^uint(0)>>1)-pageSize {
		return nil, 0, 0, 0, "", domain.ErrInvalid
	}
	var total, libraryTotal int64
	var items []domain.Artist
	var err error
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE review_status IN ('approved','blocked')`).Scan(&libraryTotal); err != nil {
		return nil, 0, 0, 0, "", err
	}
	if tag == "" {
		countSQL := `SELECT COUNT(*) FROM artists a WHERE a.review_status IN ('approved','blocked') AND (?='' OR EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.account_id=? AND ac.deleted=0))`
		if err := s.db.QueryRowContext(ctx, countSQL, accountID, accountID).Scan(&total); err != nil {
			return nil, 0, 0, 0, "", err
		}
		items, err = s.artistTagQuery(ctx, "", accountID, 0, offset, pageSize)
	} else {
		matches, err := s.artistTagQuery(ctx, tag, accountID, 0, 0, 0)
		if err != nil {
			return nil, 0, 0, 0, "", err
		}
		total = int64(len(matches))
		if offset < len(matches) {
			end := min(offset+pageSize, len(matches))
			items = matches[offset:end]
		} else {
			items = []domain.Artist{}
		}
	}
	if err != nil {
		return nil, 0, 0, 0, "", err
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	next := ""
	if page < totalPages && len(items) > 0 {
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return items, total, libraryTotal, totalPages, next, nil
}

func (s *Store) artistTagQuery(ctx context.Context, tag, accountID string, after int64, offset, limit int) ([]domain.Artist, error) {
	paged := tag == ""
	query := selectedArtistsCTE + artistTagsCTEs
	args := []any{accountID, accountID, after}
	if paged {
		query = pagedArtistsCTE + artistTagsCTEs
		args = []any{accountID, accountID, after, limit, offset}
	}
	query += ` SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed,
 COALESCE((SELECT json_group_array(display_name) FROM (SELECT display_name FROM effective_tags e WHERE e.artist_id=a.id ORDER BY e.tag_order)), '[]')
 FROM selected_artists a ORDER BY a.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Artist, 0, max(limit, 0))
	for rows.Next() {
		var a domain.Artist
		var rawTags string
		if err := rows.Scan(&a.ID, &a.Name, &a.Account, &a.AvatarURL, &a.ReviewStatus, &a.CollectionEnabled, &a.RemoteFollowed, &rawTags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rawTags), &a.Tags); err != nil {
			return nil, err
		}
		if a.Tags == nil {
			a.Tags = []string{}
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if tag != "" {
		want := strings.ToLower(tag)
		matched := items[:0]
		for _, artist := range items {
			for _, candidate := range artist.Tags {
				if strings.ToLower(candidate) == want {
					matched = append(matched, artist)
					break
				}
			}
		}
		items = matched
	}
	if !paged {
		if offset >= len(items) {
			return []domain.Artist{}, nil
		}
		end := len(items)
		if limit > 0 && end > offset+limit {
			end = offset + limit
		}
		return items[offset:end], nil
	}
	return items, nil
}

// SetArtistTagOverride sets a manual value, including an explicit empty list.
func (s *Store) SetArtistTagOverride(ctx context.Context, artistID int64, tags []string) error {
	if artistID <= 0 || len(tags) > 64 {
		return domain.ErrInvalid
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM artists WHERE id=?`, artistID).Scan(&exists); err == sql.ErrNoRows {
		return domain.ErrNotFound
	} else if err != nil {
		return err
	}
	clean := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, raw := range tags {
		t := strings.TrimSpace(raw)
		key := strings.ToLower(t)
		if t == "" || len([]rune(t)) > 80 {
			return domain.ErrInvalid
		}
		if !seen[key] {
			clean = append(clean, t)
			seen[key] = true
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO artist_tag_overrides(artist_id,tags_json,updated_at) VALUES(?,?,?) ON CONFLICT(artist_id) DO UPDATE SET tags_json=excluded.tags_json,updated_at=excluded.updated_at`, artistID, string(raw), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return domain.ErrNotFound
	}
	return nil
}

// ClearArtistTagOverride restores automatic tags.
func (s *Store) ClearArtistTagOverride(ctx context.Context, artistID int64) error {
	if artistID <= 0 {
		return domain.ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM artist_tag_overrides WHERE artist_id=?`, artistID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists int
		if e := s.db.QueryRowContext(ctx, `SELECT 1 FROM artists WHERE id=?`, artistID).Scan(&exists); e == sql.ErrNoRows {
			return domain.ErrNotFound
		} else if e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) ArtistTags(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT display_name FROM tags ORDER BY normalized_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	manual, err := s.db.QueryContext(ctx, `SELECT tags_json FROM artist_tag_overrides`)
	if err != nil {
		return nil, err
	}
	defer manual.Close()
	seen := map[string]string{}
	for _, v := range out {
		seen[strings.ToLower(v)] = v
	}
	for manual.Next() {
		var raw string
		if err = manual.Scan(&raw); err != nil {
			return nil, err
		}
		var ts []string
		if err = json.Unmarshal([]byte(raw), &ts); err != nil {
			return nil, err
		}
		for _, v := range ts {
			seen[strings.ToLower(v)] = v
		}
	}
	out = out[:0]
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, manual.Err()
}
