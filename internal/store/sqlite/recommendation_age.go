package sqlite

import (
	"context"
	"database/sql"
)

// recommendationAgeClass uses at most 30 recent visible works. Unknown is
// retained unless both the minimum count and known-ratio thresholds are met.
func recommendationAgeClass(ratings []string) string {
	if len(ratings) > 30 {
		ratings = ratings[:30]
	}
	known, adult := 0, 0
	for _, rating := range ratings {
		switch rating {
		case "safe":
			known++
		case "r18", "r18g":
			known++
			adult++
		}
	}
	if known < 8 || known*4 < len(ratings)*3 {
		return "unknown"
	}
	if adult == 0 {
		return "safe"
	}
	if adult*10 < known*3 {
		return "mixed"
	}
	return "adult"
}

func recommendationArtistAge(ctx context.Context, tx *sql.Tx, artistID int64) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT rating FROM artworks WHERE artist_id=? AND visible=1 AND metadata_state<>'unavailable' ORDER BY COALESCE(published_at,0) DESC,id DESC LIMIT 30`, artistID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ratings := []string{}
	for rows.Next() {
		var rating string
		if err = rows.Scan(&rating); err != nil {
			return "", err
		}
		ratings = append(ratings, rating)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return recommendationAgeClass(ratings), nil
}

func (s *Store) RecommendationArtistAge(ctx context.Context, artistID int64) (string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	class, err := recommendationArtistAge(ctx, tx, artistID)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return class, nil
}
