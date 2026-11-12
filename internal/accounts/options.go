package accounts

import (
	"context"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// Configure changes explicit account choices without exposing credentials.
func (m *Manager) Configure(ctx context.Context, id string, makeDefault bool, followOnApprove *bool) error {
	if !makeDefault && followOnApprove == nil {
		return domain.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	repository, ok := m.store.(interface {
		ConfigureAccount(context.Context, string, bool, *bool) error
	})
	if !ok {
		return domain.ErrInvalid
	}
	if err := repository.ConfigureAccount(ctx, id, makeDefault, followOnApprove); err != nil {
		return err
	}
	list, err := m.store.Accounts(ctx)
	if err != nil {
		return err
	}
	m.order = m.order[:0]
	for _, account := range list {
		if m.clients[account.ID] != nil {
			m.order = append(m.order, account.ID)
		}
	}
	return nil
}

func (m *Manager) CopyFollowing(ctx context.Context, source, target string) (int, error) {
	repository, ok := m.store.(interface {
		QueueFollowCopy(context.Context, string, string) (int, error)
	})
	if !ok {
		return 0, domain.ErrInvalid
	}
	return repository.QueueFollowCopy(ctx, source, target)
}

func (m *Manager) FollowArtist(ctx context.Context, artistID int64) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, err := m.client(ctx)
	if err != nil {
		return err
	}
	return client.FollowArtist(ctx, artistID)
}
