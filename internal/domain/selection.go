package domain

import "errors"

var ErrCatalogEmpty = errors.New("catalog empty")
var ErrNoMatch = errors.New("no matching image")

type SelectionRules struct {
	AllowedRatings []string   `json:"allowed_ratings"`
	AllowedAI      []string   `json:"allowed_ai"`
	IncludeTags    [][]string `json:"included_tags"`
	ExcludeTags    []string   `json:"excluded_tags"`
	MinWidth       int        `json:"min_width"`
	MinHeight      int        `json:"min_height"`
	Orientation    string     `json:"orientation"`
	Strategy       string     `json:"strategy"`
}

func DefaultRules() SelectionRules {
	return SelectionRules{AllowedRatings: []string{"safe"}, AllowedAI: []string{"non_ai"}, Orientation: "any", Strategy: "balanced"}
}

type SelectionRequest struct {
	SelectionRules
	ArtistID  int64
	AccountID string
}
type SelectedPage struct {
	IllustID    int64  `json:"illust_id,string"`
	ArtistID    int64  `json:"artist_id,string"`
	PageIndex   int    `json:"page_index"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	OriginalURL string `json:"-"`
	PreviewURL  string `json:"-"`
	SelectionID string `json:"selection_id"`
	Repeated    bool   `json:"repeated"`
}

func ValidateRules(r SelectionRules) error {
	if r.MinWidth < 0 || r.MinHeight < 0 || r.MinWidth > 100000 || r.MinHeight > 100000 || len(r.IncludeTags) > 20 || len(r.ExcludeTags) > 100 {
		return ErrInvalid
	}
	for _, s := range r.AllowedRatings {
		if s != "safe" && s != "r18" && s != "r18g" {
			return ErrInvalid
		}
	}
	for _, s := range r.AllowedAI {
		if s != "non_ai" && s != "ai" {
			return ErrInvalid
		}
	}
	switch r.Orientation {
	case "", "any", "portrait", "landscape", "square":
	default:
		return ErrInvalid
	}
	switch r.Strategy {
	case "", "balanced", "random":
	default:
		return ErrInvalid
	}
	for _, g := range r.IncludeTags {
		if len(g) == 0 || len(g) > 20 {
			return ErrInvalid
		}
		for _, t := range g {
			if t == "" || len(t) > 256 {
				return ErrInvalid
			}
		}
	}
	for _, t := range r.ExcludeTags {
		if t == "" || len(t) > 256 {
			return ErrInvalid
		}
	}
	return nil
}
