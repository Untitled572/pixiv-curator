package accounts

import (
	"context"

	"github.com/Untitled572/pixiv-curator/internal/pixiv"
)

// RelatedArtistPreviews routes the request through the account selected by the
// request context, using the same proxy, credentials, and cooldown as other API calls.
func (m *Manager) RelatedArtistPreviews(ctx context.Context, seedID int64) ([]pixiv.UserPreview, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, err := m.client(ctx)
	if err != nil {
		return nil, err
	}
	return c.RelatedArtistPreviews(ctx, seedID)
}
