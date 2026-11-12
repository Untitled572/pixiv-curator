package pixiv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// FollowArtist adds a public follow for artistID through Pixiv's fixed App API.
func (c *Client) FollowArtist(ctx context.Context, artistID int64) error {
	return c.FollowArtistChecked(ctx, artistID, nil)
}

// FollowArtistChecked runs before immediately before each follow POST. It is
// called after local request pacing so callers can revalidate state after waits.
func (c *Client) FollowArtistChecked(ctx context.Context, artistID int64, before func(context.Context) error) error {
	if artistID <= 0 {
		return &Error{Code: "INVALID_REQUEST"}
	}
	visibility := domain.ContextFollowVisibility(ctx)
	if !domain.ValidFollowVisibility(visibility) {
		return &Error{Code: "INVALID_REQUEST"}
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}

	token, err := c.accessToken(ctx, "")
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if delay := time.Until(c.nextRequest); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if before != nil {
			if err = before(ctx); err != nil {
				return err
			}
		}

		form := url.Values{
			"user_id":  {strconv.FormatInt(artistID, 10)},
			"restrict": {visibility},
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiOrigin+"/v1/user/follow/add", strings.NewReader(form.Encode()))
		setAppHeaders(req)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, requestErr := c.http.Do(req)
		c.nextRequest = time.Now().Add(2 * time.Second)
		if requestErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &Error{Code: "NETWORK_ERROR"}
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			token, err = c.accessToken(ctx, token)
			if err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			apiErr := responseError(resp)
			resp.Body.Close()
			if apiErr.Status == http.StatusTooManyRequests {
				c.mu.Lock()
				c.cooldown = time.Now().Add(apiErr.RetryAfter)
				c.mu.Unlock()
			}
			return apiErr
		}

		var payload map[string]json.RawMessage
		decodeErr := decodeResponse(resp.Body, &payload)
		resp.Body.Close()
		if decodeErr != nil || payload == nil {
			return &Error{Code: "INVALID_RESPONSE"}
		}
		rawError, ok := payload["error"]
		if !ok {
			return nil
		}
		switch strings.TrimSpace(string(rawError)) {
		case "false", "null":
			return nil
		case "true":
			return &Error{Code: "UPSTREAM_ERROR"}
		default:
			var details map[string]json.RawMessage
			if json.Unmarshal(rawError, &details) != nil || details == nil {
				return &Error{Code: "INVALID_RESPONSE"}
			}
			if len(details) > 0 {
				return &Error{Code: "UPSTREAM_ERROR"}
			}
			return nil
		}
	}
	return &Error{Code: "AUTH_REQUIRED", Status: http.StatusUnauthorized}
}
