package accounts

import "context"

// FollowArtistChecked routes a public follow through the selected account and
// revalidates caller state after Pixiv request pacing, immediately before POST.
func (m *Manager) FollowArtistChecked(ctx context.Context, artistID int64, before func(context.Context) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, err := m.client(ctx)
	if err != nil {
		return err
	}
	return c.FollowArtistChecked(ctx, artistID, before)
}
