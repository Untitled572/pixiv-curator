package pixiv

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

var explorationModes = map[string]struct{}{
	"day": {}, "week": {}, "month": {}, "day_male": {}, "day_female": {},
	"week_original": {}, "week_rookie": {}, "day_r18": {}, "week_r18": {},
}

// ExplorationArtworks reads one bounded page of the independent Pixiv ranking
// source. It is a historical ranking, not a uniformly random sample of Pixiv.
func (c *Client) ExplorationArtworks(ctx context.Context, mode, date string, offset int) ([]RawArtwork, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := explorationModes[mode]; !ok {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date || parsed.After(time.Now().UTC().Truncate(24*time.Hour).Add(-24*time.Hour)) {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	if offset < 0 || offset > 450 || offset%30 != 0 {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	query := url.Values{}
	query.Set("mode", mode)
	query.Set("date", date)
	query.Set("offset", strconv.Itoa(offset))
	query.Set("filter", "for_ios")
	var raw struct {
		Artworks []RawArtwork `json:"illusts"`
	}
	err = c.get(ctx, fmt.Sprintf("%s/v1/illust/ranking?%s", apiOrigin, query.Encode()), &raw)
	return raw.Artworks, err
}
