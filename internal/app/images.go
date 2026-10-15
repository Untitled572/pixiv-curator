package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type ImageRepository interface {
	Select(context.Context, domain.SelectionRequest) (domain.SelectedPage, error)
	EligiblePage(context.Context, int64, int, bool) (domain.SelectedPage, error)
	Setting(context.Context, string) (json.RawMessage, int64, error)
	PutSetting(context.Context, string, json.RawMessage, int64) error
	Enqueue(context.Context, string, string, json.RawMessage) (string, error)
	SetArtworkPreference(context.Context, int64, string) error
}
type Images struct {
	store ImageRepository
	base  string
}

func NewImages(s ImageRepository, base string) *Images { return &Images{store: s, base: base} }

type ImageResult struct {
	domain.SelectedPage
	URL string `json:"url"`
}

func (s *Images) Select(ctx context.Context, r domain.SelectionRequest) (ImageResult, error) {
	p, err := s.store.Select(ctx, r)
	if err == nil && !domain.ValidImageURL(p.OriginalURL) {
		return ImageResult{}, domain.ErrNotFound
	}
	return ImageResult{SelectedPage: p, URL: p.OriginalURL}, err
}
func (s *Images) Page(ctx context.Context, id int64, index int, preview bool) (domain.SelectedPage, error) {
	if id <= 0 || index < 0 {
		return domain.SelectedPage{}, domain.ErrInvalid
	}
	p, err := s.store.EligiblePage(ctx, id, index, preview)
	if err != nil {
		return domain.SelectedPage{}, err
	}
	url := p.OriginalURL
	if preview {
		url = p.PreviewURL
	}
	if !domain.ValidImageURL(url) {
		return domain.SelectedPage{}, domain.ErrNotFound
	}
	return p, nil
}
func (s *Images) Repair(ctx context.Context, id int64) error {
	p, _ := json.Marshal(map[string]any{"illust_id": id})
	_, err := s.store.Enqueue(ctx, "repair_media", fmt.Sprintf("repair:%d", id), p)
	return err
}
func (s *Images) Preferences(ctx context.Context) (domain.SelectionRules, int64, error) {
	raw, v, err := s.store.Setting(ctx, "preferences")
	if errors.Is(err, domain.ErrNotFound) {
		return domain.DefaultRules(), 0, nil
	}
	var rules domain.SelectionRules
	if err != nil {
		return rules, v, err
	}
	if json.Unmarshal(raw, &rules) != nil {
		return rules, v, domain.ErrInvalid
	}
	return rules, v, domain.ValidateRules(rules)
}
func (s *Images) PutPreferences(ctx context.Context, r domain.SelectionRules, v int64) error {
	if len(r.AllowedRatings) == 0 || len(r.AllowedAI) == 0 {
		return domain.ErrInvalid
	}
	if r.Strategy == "" {
		r.Strategy = "balanced"
	}
	if r.Orientation == "" {
		r.Orientation = "any"
	}
	if err := domain.ValidateRules(r); err != nil {
		return err
	}
	raw, _ := json.Marshal(r)
	return s.store.PutSetting(ctx, "preferences", raw, v)
}
func (s *Images) SetPreference(ctx context.Context, id int64, value string) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	switch value {
	case "like", "dislike", "neutral":
	default:
		return domain.ErrInvalid
	}
	return s.store.SetArtworkPreference(ctx, id, value)
}
