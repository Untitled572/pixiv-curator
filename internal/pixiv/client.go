package pixiv

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/secret"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiOrigin = "https://app-api.pixiv.net"
const oauthURL = "https://oauth.secure.pixiv.net/auth/token"

type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	HashSecret   string `json:"hash_secret"`
}

type oauthID int64

func (i *oauthID) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseInt(strings.Trim(string(b), "\""), 10, 64)
	if err != nil || v <= 0 {
		return &Error{Code: "INVALID_RESPONSE"}
	}
	*i = oauthID(v)
	return nil
}

func ReadOAuthConfig(path string) (OAuthConfig, error) {
	var c OAuthConfig
	b, err := secret.ReadBytes(path)
	if err != nil {
		return c, err
	}
	if json.Unmarshal(b, &c) != nil || c.ClientID == "" || c.ClientSecret == "" {
		return OAuthConfig{}, secret.ErrFile
	}
	return c, nil
}

type Error struct {
	Code       string
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return "pixiv: " + e.Code }

type Client struct {
	http        *http.Client
	tokenFile   string
	oauth       OAuthConfig
	mu          sync.Mutex
	access      string
	expires     time.Time
	accountID   int64
	state       string
	blocked     bool
	refreshing  chan struct{}
	refreshErr  error
	cooldown    time.Time
	gate        chan struct{}
	nextRequest time.Time
}

// NewClient restricts every request to fixed official origins. Tests substitute
// a transport, never a production endpoint or real account.
func NewClient(tokenFile string, oauth OAuthConfig, transport http.RoundTripper) *Client {
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	state := "configured"
	if tokenFile == "" || oauth.ClientID == "" || oauth.ClientSecret == "" {
		state = "expired"
	}
	return &Client{tokenFile: tokenFile, oauth: oauth, state: state, gate: make(chan struct{}, 1), http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return &Error{Code: "REDIRECT_REJECTED"} }}}
}
func (c *Client) Status() string { c.mu.Lock(); defer c.mu.Unlock(); return c.state }
func (c *Client) Credentials(ctx context.Context, token string) error {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenFile == "" {
		return &Error{Code: "ACCOUNT_NOT_CONFIGURED"}
	}
	if err := secret.Write(c.tokenFile, token); err != nil {
		c.state = "error"
		return &Error{Code: "CREDENTIAL_SAVE_FAILED"}
	}
	c.access = ""
	c.expires = time.Time{}
	c.blocked = false
	c.state = "configured"
	return nil
}

func (c *Client) accessToken(ctx context.Context, rejected string) (string, error) {
	c.mu.Lock()
	if remaining := time.Until(c.cooldown); remaining > 0 {
		c.mu.Unlock()
		return "", &Error{Code: "RATE_LIMITED", Status: 429, RetryAfter: remaining}
	}
	if c.blocked {
		c.mu.Unlock()
		return "", &Error{Code: "CREDENTIAL_SAVE_FAILED"}
	}
	if c.access != "" && time.Now().Add(30*time.Second).Before(c.expires) && (rejected == "" || c.access != rejected) {
		token := c.access
		c.mu.Unlock()
		return token, nil
	}
	if wait := c.refreshing; wait != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-wait:
		}
		c.mu.Lock()
		token, err := c.access, c.refreshErr
		c.mu.Unlock()
		return token, err
	}
	wait := make(chan struct{})
	c.refreshing = wait
	c.mu.Unlock()
	token, rotated, expiry, account, err := c.refresh(ctx)
	if err == nil && rotated != "" {
		if secret.Write(c.tokenFile, rotated) != nil {
			err = &Error{Code: "CREDENTIAL_SAVE_FAILED"}
			c.mu.Lock()
			c.blocked = true
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshErr = err
	if err == nil {
		c.access = token
		c.expires = expiry
		c.accountID = account
		c.state = "ready"
	} else {
		c.access = ""
		c.state = "error"
		var e *Error
		if errors.As(err, &e) && e.Status == 401 {
			c.state = "expired"
		}
		if errors.As(err, &e) && e.Status == 429 {
			c.cooldown = time.Now().Add(e.RetryAfter)
		}
	}
	c.refreshing = nil
	close(wait)
	return c.access, err
}
func (c *Client) refresh(ctx context.Context) (string, string, time.Time, int64, error) {
	token, err := secret.Read(c.tokenFile)
	if err != nil || c.oauth.ClientID == "" || c.oauth.ClientSecret == "" {
		return "", "", time.Time{}, 0, &Error{Code: "AUTH_REQUIRED", Status: 401}
	}
	form := url.Values{"client_id": {c.oauth.ClientID}, "client_secret": {c.oauth.ClientSecret}, "grant_type": {"refresh_token"}, "refresh_token": {token}, "get_secure_url": {"1"}, "include_policy": {"1"}}
	req, _ := http.NewRequestWithContext(ctx, "POST", oauthURL, strings.NewReader(form.Encode()))
	setAppHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.oauth.HashSecret != "" {
		now := time.Now().UTC().Format(time.RFC3339)
		hash := md5.Sum([]byte(now + c.oauth.HashSecret))
		req.Header.Set("X-Client-Time", now)
		req.Header.Set("X-Client-Hash", hex.EncodeToString(hash[:]))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", time.Time{}, 0, &Error{Code: "NETWORK_ERROR"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", time.Time{}, 0, responseError(resp)
	}
	var p struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		User    struct {
			ID oauthID `json:"id"`
		} `json:"user"`
		Response json.RawMessage `json:"response"`
	}
	if err = decodeResponse(resp.Body, &p); err != nil {
		return "", "", time.Time{}, 0, err
	}
	if len(p.Response) > 0 {
		if json.Unmarshal(p.Response, &p) != nil {
			return "", "", time.Time{}, 0, &Error{Code: "INVALID_RESPONSE"}
		}
	}
	if p.Access == "" || len(p.Access) > 16384 || p.Expires <= 0 || p.Expires > 86400 || p.User.ID <= 0 || strings.ContainsAny(p.Access, "\r\n") {
		return "", "", time.Time{}, 0, &Error{Code: "INVALID_RESPONSE"}
	}
	return p.Access, p.Refresh, time.Now().Add(time.Duration(p.Expires) * time.Second), int64(p.User.ID), nil
}
func setAppHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "PixivAndroidApp/5.0.234 (Android 11; Pixel 5)")
	req.Header.Set("App-OS", "android")
	req.Header.Set("App-OS-Version", "11")
	req.Header.Set("App-Version", "5.0.234")
	req.Header.Set("Accept-Language", "en_US")
}
func validateAPIURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "app-api.pixiv.net" || u.User != nil || u.Fragment != "" {
		return &Error{Code: "INVALID_NEXT_URL"}
	}
	return nil
}

func (c *Client) get(ctx context.Context, raw string, result any) error {
	if err := validateAPIURL(raw); err != nil {
		return err
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
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
		token, err := c.accessToken(ctx, "")
		if err != nil {
			return err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
		setAppHeaders(req)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.http.Do(req)
		c.nextRequest = time.Now().Add(time.Second)
		if err != nil {
			return &Error{Code: "NETWORK_ERROR"}
		}
		if resp.StatusCode == 401 && attempt == 0 {
			resp.Body.Close()
			if _, err = c.accessToken(ctx, token); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != 200 {
			e := responseError(resp)
			resp.Body.Close()
			if e.Status == 429 {
				c.mu.Lock()
				c.cooldown = time.Now().Add(e.RetryAfter)
				c.mu.Unlock()
			}
			return e
		}
		err = decodeResponse(resp.Body, result)
		resp.Body.Close()
		return err
	}
	return &Error{Code: "AUTH_REQUIRED", Status: 401}
}
func decodeResponse(body io.Reader, result any) error {
	data, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
	if err != nil {
		return &Error{Code: "NETWORK_ERROR"}
	}
	if len(data) > 8<<20 || json.Unmarshal(data, result) != nil {
		return &Error{Code: "INVALID_RESPONSE"}
	}
	return nil
}
func responseError(resp *http.Response) *Error {
	e := &Error{Status: resp.StatusCode, Code: "UPSTREAM_ERROR"}
	switch resp.StatusCode {
	case 401:
		e.Code = "AUTH_REQUIRED"
	case 429:
		e.Code = "RATE_LIMITED"
		e.RetryAfter = time.Minute
		if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 && sec <= 86400 {
			e.RetryAfter = time.Duration(sec) * time.Second
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && time.Until(date) > 0 {
			e.RetryAfter = time.Until(date)
		}
	case 404:
		e.Code = "NOT_FOUND"
	case 403:
		e.Code = "ACCESS_RESTRICTED"
	}
	return e
}

type FollowingPage struct {
	Artists []RawArtist
	Next    string
}

func (c *Client) Following(ctx context.Context, next string) (FollowingPage, error) {
	if next == "" {
		select {
		case c.gate <- struct{}{}:
		case <-ctx.Done():
			return FollowingPage{}, ctx.Err()
		}
		_, err := c.accessToken(ctx, "")
		c.mu.Lock()
		id := c.accountID
		c.mu.Unlock()
		<-c.gate
		if err != nil {
			return FollowingPage{}, err
		}
		next = apiOrigin + "/v1/user/following?restrict=public&user_id=" + strconv.FormatInt(id, 10)
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
	if raw.Next != "" {
		if err := validateAPIURL(raw.Next); err != nil {
			return FollowingPage{}, err
		}
	}
	out := FollowingPage{Next: raw.Next}
	current, _ := url.Parse(next)
	if raw.Next == "" && current.Query().Get("restrict") == "public" {
		private := *current
		q := private.Query()
		q.Set("restrict", "private")
		q.Del("offset")
		private.RawQuery = q.Encode()
		out.Next = private.String()
	}
	for _, a := range raw.Users {
		out.Artists = append(out.Artists, a.User)
	}
	return out, nil
}

type ArtworkPage struct {
	Artworks []RawArtwork
	Next     string
}

func (c *Client) ArtistArtworks(ctx context.Context, id int64, next string) (ArtworkPage, error) {
	if id <= 0 {
		return ArtworkPage{}, &Error{Code: "INVALID_REQUEST"}
	}
	if next == "" {
		next = apiOrigin + "/v1/user/illusts?filter=for_ios&type=illust&user_id=" + strconv.FormatInt(id, 10)
	}
	var raw struct {
		Artworks []RawArtwork `json:"illusts"`
		Next     string       `json:"next_url"`
	}
	if err := c.get(ctx, next, &raw); err != nil {
		return ArtworkPage{}, err
	}
	if raw.Next != "" {
		if err := validateAPIURL(raw.Next); err != nil {
			return ArtworkPage{}, err
		}
	}
	current, _ := url.Parse(next)
	if raw.Next == "" && (current.Query().Get("type") == "illust" || current.Query().Get("type") == "") {
		manga := *current
		q := manga.Query()
		q.Set("type", "manga")
		q.Del("offset")
		manga.RawQuery = q.Encode()
		raw.Next = manga.String()
	}
	return ArtworkPage{Artworks: raw.Artworks, Next: raw.Next}, nil
}
func (c *Client) Artwork(ctx context.Context, id int64) (RawArtwork, error) {
	var raw struct {
		Artwork RawArtwork `json:"illust"`
	}
	if id <= 0 {
		return raw.Artwork, &Error{Code: "INVALID_REQUEST"}
	}
	err := c.get(ctx, apiOrigin+"/v1/illust/detail?illust_id="+strconv.FormatInt(id, 10), &raw)
	return raw.Artwork, err
}

func (c *Client) RelatedArtists(ctx context.Context, id int64) ([]RawArtist, error) {
	if id <= 0 {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	var raw struct {
		Users []struct {
			User RawArtist `json:"user"`
		} `json:"user_previews"`
	}
	if err := c.get(ctx, apiOrigin+"/v1/user/related?filter=for_ios&offset=0&seed_user_id="+strconv.FormatInt(id, 10), &raw); err != nil {
		return nil, err
	}
	result := []RawArtist{}
	for _, a := range raw.Users {
		result = append(result, a.User)
	}
	return result, nil
}
func (c *Client) RelatedArtworks(ctx context.Context, id int64) ([]RawArtwork, error) {
	if id <= 0 {
		return nil, &Error{Code: "INVALID_REQUEST"}
	}
	var raw struct {
		Artworks []RawArtwork `json:"illusts"`
	}
	if err := c.get(ctx, apiOrigin+"/v2/illust/related?filter=for_ios&illust_id="+strconv.FormatInt(id, 10), &raw); err != nil {
		return nil, err
	}
	return raw.Artworks, nil
}

// Authenticate validates credentials and returns the upstream account identity.
func (c *Client) Authenticate(ctx context.Context) (int64, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if _, err := c.accessToken(ctx, ""); err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accountID, nil
}

// NextArtworkSection skips already indexed older pages while still checking manga.
func NextArtworkSection(current, next string) (string, error) {
	if next == "" {
		return "", nil
	}
	if err := validateAPIURL(next); err != nil {
		return "", err
	}
	if current != "" {
		if err := validateAPIURL(current); err != nil {
			return "", err
		}
		u, _ := url.Parse(current)
		if u.Query().Get("type") == "manga" {
			return "", nil
		}
	}
	u, _ := url.Parse(next)
	if u.Query().Get("type") == "manga" {
		return next, nil
	}
	q := u.Query()
	q.Set("type", "manga")
	q.Del("offset")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
