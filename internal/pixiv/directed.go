package pixiv

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) SearchTaggedArtworks(ctx context.Context, tag string, offset int) ([]RawArtwork, error) {
	if strings.TrimSpace(tag) == "" || len(tag) > 300 || offset < 0 || offset > 450 || offset%30 != 0 {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	params := url.Values{
		"word": {tag}, "search_target": {"exact_match_for_tags"},
		"sort": {"date_desc"}, "filter": {"for_ios"}, "offset": {strconv.Itoa(offset)},
	}
	var result struct {
		Artworks []RawArtwork `json:"illusts"`
	}
	err := c.get(ctx, apiOrigin+"/v1/search/illust?"+params.Encode(), &result)
	return result.Artworks, err
}
