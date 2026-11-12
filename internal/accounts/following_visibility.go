package accounts

import (
	"context"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
)

// FollowingRestricted routes one public or private follow-list phase through the
// selected account and binds its verified Pixiv identity just like Following.
func (m *Manager) FollowingRestricted(ctx context.Context, next, visibility string) (pixiv.FollowingPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, err := m.client(ctx)
	if err != nil {
		return pixiv.FollowingPage{}, err
	}
	page, err := c.FollowingRestricted(ctx, next, visibility)
	if err == nil && domain.ContextAccount(ctx) != "" {
		id, authErr := c.Authenticate(ctx)
		if authErr != nil {
			return pixiv.FollowingPage{}, authErr
		}
		if err = m.store.BindAccountIdentity(ctx, domain.ContextAccount(ctx), id); err != nil {
			return pixiv.FollowingPage{}, err
		}
	}
	return page, err
}
