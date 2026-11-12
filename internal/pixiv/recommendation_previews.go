package pixiv

import (
	"context"
	"net/url"
	"strconv"
)

// UserPreview contains a related artist and the artworks returned with that
// user by Pixiv's user-related endpoint.
type UserPreview struct {
	User    RawArtist    `json:"user"`
	Illusts []RawArtwork `json:"illusts"`
}

// RelatedArtistPreviews returns Pixiv's related users together with their
// embedded artwork previews, avoiding a separate artwork request per user.
func (c *Client) RelatedArtistPreviews(ctx context.Context, seedID int64) ([]UserPreview, error) {
	if seedID <= 0 {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	query := url.Values{}
	query.Set("filter", "for_ios")
	query.Set("offset", "0")
	query.Set("seed_user_id", strconv.FormatInt(seedID, 10))
	var raw struct {
		Previews []UserPreview `json:"user_previews"`
	}
	if err := c.get(ctx, apiOrigin+"/v1/user/related?"+query.Encode(), &raw); err != nil {
		return nil, err
	}
	if raw.Previews == nil {
		raw.Previews = []UserPreview{}
	}
	return raw.Previews, nil
}
