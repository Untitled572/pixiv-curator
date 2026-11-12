package pixiv

import (
	"context"
	"net/url"
	"strconv"
)

// FollowingRestricted lists the current authenticated user's follows for exactly
// one visibility phase. Continuation URLs are constrained to that identity and phase.
func (c *Client) FollowingRestricted(ctx context.Context, next, visibility string) (FollowingPage, error) {
	if visibility != "public" && visibility != "private" {
		return FollowingPage{}, &Error{Code: "INVALID_REQUEST"}
	}
	id, err := c.Authenticate(ctx)
	if err != nil {
		return FollowingPage{}, err
	}
	if id <= 0 {
		return FollowingPage{}, &Error{Code: "INVALID_RESPONSE"}
	}
	if next == "" {
		next = apiOrigin + "/v1/user/following?restrict=" + visibility + "&user_id=" + strconv.FormatInt(id, 10)
	}
	next, err = validateFollowingURL(next, id, visibility)
	if err != nil {
		return FollowingPage{}, err
	}
	var raw struct {
		Users []struct {
			User RawArtist `json:"user"`
		} `json:"user_previews"`
		Next string `json:"next_url"`
	}
	if err := c.get(ctx, next, &raw); err != nil {
		return FollowingPage{}, err
	}
	out := FollowingPage{}
	if raw.Next != "" {
		out.Next, err = validateFollowingURL(raw.Next, id, visibility)
		if err != nil {
			return FollowingPage{}, err
		}
	}
	for _, a := range raw.Users {
		out.Artists = append(out.Artists, a.User)
	}
	return out, nil
}

func validateFollowingURL(raw string, ownID int64, visibility string) (string, error) {
	if err := validateAPIURL(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path != "/v1/user/following" || u.EscapedPath() != "/v1/user/following" {
		return "", &Error{Code: "INVALID_NEXT_URL"}
	}
	q := u.Query()
	if _, hasPhase := q["phase"]; hasPhase {
		return "", &Error{Code: "INVALID_NEXT_URL"}
	}
	users, hasUser := q["user_id"]
	if hasUser && (len(users) != 1 || users[0] != strconv.FormatInt(ownID, 10)) {
		return "", &Error{Code: "INVALID_NEXT_URL"}
	}
	restricts, hasRestrict := q["restrict"]
	if hasRestrict && (len(restricts) != 1 || restricts[0] != visibility) {
		return "", &Error{Code: "INVALID_NEXT_URL"}
	}
	if !hasUser {
		q.Set("user_id", strconv.FormatInt(ownID, 10))
	}
	if !hasRestrict {
		q.Set("restrict", visibility)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
