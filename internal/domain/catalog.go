package domain

import "errors"

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("state conflict")
	ErrInvalid  = errors.New("invalid input")
	ErrLease    = errors.New("lease lost")
)

type Artist struct {
	ID                int64  `json:"id,string"`
	Name              string `json:"name"`
	Account           string `json:"account"`
	AvatarURL         string `json:"avatar_url"`
	ReviewStatus      string `json:"review_status"`
	CollectionEnabled bool   `json:"collection_enabled"`
	RemoteFollowed    bool   `json:"remote_followed"`
}

type Page struct {
	Index       int    `json:"page_index"`
	OriginalURL string `json:"-"`
	PreviewURL  string `json:"-"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	State       string `json:"media_state"`
}

type Artwork struct {
	ID            int64    `json:"id,string"`
	ArtistID      int64    `json:"artist_id,string"`
	Title         string   `json:"title"`
	Caption       string   `json:"caption"`
	Kind          string   `json:"kind"`
	Rating        string   `json:"rating"`
	AIStatus      string   `json:"ai_status"`
	PageCount     int      `json:"page_count"`
	Bookmarks     *int64   `json:"bookmarks"`
	Views         *int64   `json:"views"`
	PublishedAt   *int64   `json:"published_at"`
	MetadataState string   `json:"metadata_state"`
	Visible       bool     `json:"visible"`
	Tags          []string `json:"tags"`
	Pages         []Page   `json:"pages"`
}
