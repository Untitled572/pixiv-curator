// Package app contains use cases shared by HTTP and background work.
package app

import (
	"context"
	"encoding/json"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type CatalogRepository interface {
	UpsertArtist(context.Context, domain.Artist, string, string) error
	Artists(context.Context, int64, int) ([]domain.Artist, error)
	DecideArtist(context.Context, int64, string) (string, error)
	SetCollection(context.Context, int64, bool) error
	Jobs(context.Context, string, int) ([]domain.Job, error)
	Cancel(context.Context, string) error
	Enqueue(context.Context, string, string, json.RawMessage) (string, error)
}

type Catalog struct{ repository CatalogRepository }

func NewCatalog(repository CatalogRepository) *Catalog { return &Catalog{repository: repository} }
func (s *Catalog) ImportArtist(ctx context.Context, id int64) error {
	if id <= 0 {
		return domain.ErrInvalid
	}
	return s.repository.UpsertArtist(ctx, domain.Artist{ID: id}, "manual", "uid")
}
func (s *Catalog) Artists(ctx context.Context, after int64, limit int) ([]domain.Artist, error) {
	return s.repository.Artists(ctx, after, limit)
}
func (s *Catalog) DecideArtist(ctx context.Context, id int64, action string) (string, error) {
	if id <= 0 {
		return "", domain.ErrInvalid
	}
	return s.repository.DecideArtist(ctx, id, action)
}
func (s *Catalog) SetCollection(ctx context.Context, id int64, enabled bool) error {
	return s.repository.SetCollection(ctx, id, enabled)
}
func (s *Catalog) Jobs(ctx context.Context, after string, limit int) ([]domain.Job, error) {
	return s.repository.Jobs(ctx, after, limit)
}
func (s *Catalog) Cancel(ctx context.Context, id string) error { return s.repository.Cancel(ctx, id) }
