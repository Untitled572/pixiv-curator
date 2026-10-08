package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// UpsertArtist updates upstream metadata only; remote data never approves an artist.
func (s *Store) UpsertArtist(ctx context.Context, a domain.Artist, source, key string) error {
	if a.ID <= 0 {
		return domain.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = upsertArtist(ctx, tx, a, source, key); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertArtist(ctx context.Context, tx *sql.Tx, a domain.Artist, source, key string) error {
	now := time.Now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO artists(id,name,account,avatar_url,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,account=excluded.account,avatar_url=excluded.avatar_url,updated_at=excluded.updated_at`, a.ID, a.Name, a.Account, a.AvatarURL, now, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artist_sources VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, a.ID, source, key, now)
	return err
}

func (s *Store) Artists(ctx context.Context, after int64, limit int) ([]domain.Artist, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,account,avatar_url,review_status,collection_enabled,remote_followed FROM artists WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Artist{}
	for rows.Next() {
		var a domain.Artist
		if err = rows.Scan(&a.ID, &a.Name, &a.Account, &a.AvatarURL, &a.ReviewStatus, &a.CollectionEnabled, &a.RemoteFollowed); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// UpsertArtwork stores a complete or partial upstream snapshot. Only complete
// snapshots replace tags and retire missing pages. Local visibility is preserved.
func (s *Store) UpsertArtwork(ctx context.Context, a domain.Artwork) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = upsertArtwork(ctx, tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertArtwork(ctx context.Context, tx *sql.Tx, a domain.Artwork) error {
	if a.ID <= 0 || a.ArtistID <= 0 || a.PageCount < 0 {
		return domain.ErrInvalid
	}
	a.Kind = enum(a.Kind, "illust", "manga", "ugoira")
	a.Rating = enum(a.Rating, "safe", "r18", "r18g")
	a.AIStatus = enum(a.AIStatus, "non_ai", "ai")
	if a.MetadataState == "" {
		a.MetadataState = "pending"
	}
	seen := map[int]bool{}
	for _, p := range a.Pages {
		if (p.State == "" || p.State == "indexed") && !domain.ValidImageURL(p.OriginalURL) {
			return domain.ErrInvalid
		}
		if p.PreviewURL != "" && !domain.ValidImageURL(p.PreviewURL) {
			return domain.ErrInvalid
		}
		if p.Index < 0 || p.Index >= a.PageCount || seen[p.Index] {
			return domain.ErrInvalid
		}
		seen[p.Index] = true
	}
	if a.MetadataState == "complete" && len(a.Pages) != a.PageCount {
		return domain.ErrInvalid
	}
	now := time.Now().UnixMilli()
	_, err := tx.ExecContext(ctx, `INSERT INTO artworks(id,artist_id,title,caption,kind,page_count,rating,ai_status,bookmarks,views,published_at,metadata_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET title=excluded.title,caption=excluded.caption,kind=excluded.kind,page_count=CASE WHEN excluded.metadata_state='complete' THEN excluded.page_count ELSE artworks.page_count END,rating=excluded.rating,ai_status=excluded.ai_status,bookmarks=excluded.bookmarks,views=excluded.views,published_at=excluded.published_at,metadata_state=excluded.metadata_state,updated_at=excluded.updated_at WHERE artworks.artist_id=excluded.artist_id`, a.ID, a.ArtistID, a.Title, a.Caption, a.Kind, a.PageCount, a.Rating, a.AIStatus, a.Bookmarks, a.Views, a.PublishedAt, a.MetadataState, now, now)
	if err != nil {
		return err
	}
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT artist_id FROM artworks WHERE id=?`, a.ID).Scan(&owner); err != nil {
		return err
	}
	if owner != a.ArtistID {
		return domain.ErrConflict
	}
	if a.MetadataState == "complete" {
		if _, err = tx.ExecContext(ctx, `UPDATE image_pages SET media_state='unavailable',updated_at=? WHERE illust_id=?`, now, a.ID); err != nil {
			return err
		}
	}
	for _, p := range a.Pages {
		if p.State == "" {
			p.State = "indexed"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO image_pages(illust_id,page_index,original_url,preview_url,width,height,media_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(illust_id,page_index) DO UPDATE SET original_url=excluded.original_url,preview_url=excluded.preview_url,width=excluded.width,height=excluded.height,media_state=excluded.media_state,updated_at=excluded.updated_at`, a.ID, p.Index, p.OriginalURL, p.PreviewURL, p.Width, p.Height, p.State, now, now)
		if err != nil {
			return err
		}
	}
	if a.MetadataState == "complete" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM artwork_tags WHERE illust_id=?`, a.ID); err != nil {
			return err
		}
		for _, tag := range a.Tags {
			key := strings.ToLower(strings.TrimSpace(tag))
			if key == "" {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO tags(normalized_name,display_name) VALUES(?,?) ON CONFLICT(normalized_name) DO NOTHING`, key, strings.TrimSpace(tag)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO artwork_tags SELECT ?,id FROM tags WHERE normalized_name=? ON CONFLICT DO NOTHING`, a.ID, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func enum(value string, known ...string) string {
	for _, k := range known {
		if value == k {
			return k
		}
	}
	return "unknown"
}

func (s *Store) Setting(ctx context.Context, key string) (json.RawMessage, int64, error) {
	var raw string
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT value_json,version FROM settings WHERE key=?`, key).Scan(&raw, &version)
	if err == sql.ErrNoRows {
		return nil, 0, domain.ErrNotFound
	}
	return json.RawMessage(raw), version, err
}

// PutSetting is compare-and-swap. Only named, non-secret settings are accepted.
func (s *Store) PutSetting(ctx context.Context, key string, value json.RawMessage, version int64) error {
	if key != "preferences" && key != "sync" && key != "recommendation" {
		return domain.ErrInvalid
	}
	if !json.Valid(value) || version < 0 {
		return domain.ErrInvalid
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value_json,version,updated_at) SELECT ?,?,1,? WHERE ?=0 ON CONFLICT(key) DO NOTHING`, key, string(value), time.Now().UnixMilli(), version)
	if err != nil {
		return err
	}
	if version > 0 {
		r, err = s.db.ExecContext(ctx, `UPDATE settings SET value_json=?,version=version+1,updated_at=? WHERE key=? AND version=?`, string(value), time.Now().UnixMilli(), key, version)
		if err != nil {
			return err
		}
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrConflict
	}
	return nil
}
