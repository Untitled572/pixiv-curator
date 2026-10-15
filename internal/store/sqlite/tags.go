package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// ArtistTagPage returns artists ordered by ID with effective manual or automatic tags.
func (s *Store) ArtistTagPage(ctx context.Context, tag, accountID string, after int64, limit int) ([]domain.Artist, error) {
	if after < 0 || limit < 1 || limit > 200 || strings.TrimSpace(tag) != tag || strings.TrimSpace(accountID) != accountID {
		return nil, domain.ErrInvalid
	}
	if tag != "" {
		tag = strings.ToLower(tag)
	}
	result := make([]domain.Artist, 0, limit+1)
	lastCandidate := after
	for len(result) < limit+1 {
		rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.name,a.account,a.avatar_url,a.review_status,a.collection_enabled,a.remote_followed,COALESCE(o.tags_json,'')
		 FROM artists a LEFT JOIN artist_tag_overrides o ON o.artist_id=a.id
			WHERE a.id>? AND a.review_status IN ('approved','blocked') AND (?='' OR EXISTS (SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.account_id=? AND ac.deleted=0))
		 ORDER BY a.id LIMIT 100`, lastCandidate, accountID, accountID)
		if err != nil {
			return nil, err
		}
		type candidate struct {
			artist   domain.Artist
			override string
		}
		candidates := make([]candidate, 0, 100)
		for rows.Next() {
			var a domain.Artist
			var override string
			if err = rows.Scan(&a.ID, &a.Name, &a.Account, &a.AvatarURL, &a.ReviewStatus, &a.CollectionEnabled, &a.RemoteFollowed, &override); err != nil {
				rows.Close()
				return nil, err
			}
			lastCandidate = a.ID
			candidates = append(candidates, candidate{artist: a, override: override})
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		for _, item := range candidates {
			a := item.artist
			if item.override != "" {
				if err = json.Unmarshal([]byte(item.override), &a.Tags); err != nil {
					return nil, err
				}
			} else {
				a.Tags, err = s.automaticArtistTags(ctx, a.ID)
				if err != nil {
					return nil, err
				}
			}
			if tag == "" || hasNormalizedTag(a.Tags, tag) {
				result = append(result, a)
			}
			if len(result) >= limit+1 {
				break
			}
		}
		if len(candidates) < 100 { // account filtering may reduce rows; check whether the candidate scan reached the end
			var more int
			if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists a WHERE a.id>? AND a.review_status IN ('approved','blocked') AND (?='' OR EXISTS(SELECT 1 FROM account_artists aa JOIN accounts ac ON ac.id=aa.account_id WHERE aa.artist_id=a.id AND aa.account_id=? AND ac.deleted=0)))`, lastCandidate, accountID, accountID).Scan(&more); err != nil {
				return nil, err
			}
			if more == 0 {
				return result, nil
			}
		}
	}
	return result, nil
}

func (s *Store) automaticArtistTags(ctx context.Context, artistID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
 SELECT id FROM artworks WHERE artist_id=? ORDER BY COALESCE(published_at,0) DESC,id DESC LIMIT 100
), counts AS (
 SELECT t.display_name,t.normalized_name,COUNT(*) AS n FROM recent r JOIN artwork_tags at ON at.illust_id=r.id JOIN tags t ON t.id=at.tag_id
 GROUP BY t.normalized_name,t.display_name ORDER BY n DESC,t.normalized_name LIMIT 8
 ) SELECT display_name FROM counts ORDER BY n DESC,normalized_name`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err = rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
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

func hasNormalizedTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.ToLower(t) == want {
			return true
		}
	}
	return false
}
