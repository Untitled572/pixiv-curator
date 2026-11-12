// Package accounts owns per-account credentials and routes metadata requests.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/pixiv"
	"github.com/Untitled572/pixiv-curator/internal/secret"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Repository interface {
	Accounts(context.Context) ([]domain.Account, error)
	SaveAccount(context.Context, domain.Account) error
	DeleteAccount(context.Context, string) error
	SyncAccount(context.Context, string) (string, error)
	LegacyImported(context.Context) (bool, error)
	AdoptLegacyFollowing(context.Context) error
	BindAccountIdentity(context.Context, string, int64) error
	DeletedAccountFiles(context.Context) ([]string, error)
}
type Manager struct {
	mu        sync.RWMutex
	store     Repository
	dir       string
	oauth     pixiv.OAuthConfig
	transport http.RoundTripper
	clients   map[string]*pixiv.Client
	order     []string
}

func Open(ctx context.Context, s Repository, dir, legacy string, oauth pixiv.OAuthConfig, t http.RoundTripper) (*Manager, error) {
	m := &Manager{store: s, dir: dir, oauth: oauth, transport: t, clients: map[string]*pixiv.Client{}}
	deleted, err := s.DeletedAccountFiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, path := range deleted {
		if err := removeOwnedCredential(dir, path); err != nil {
			slog.Warn("deleted account credential cleanup incomplete")
		}
	}
	imported, err := s.LegacyImported(ctx)
	if err != nil {
		return nil, err
	}
	if !imported && legacy != "" {
		if _, err := secret.Read(legacy); err == nil {
			if err = s.SaveAccount(ctx, domain.Account{ID: "legacy", Name: "原有 Pixiv 账号", TokenFile: legacy}); err != nil {
				return nil, err
			}
			if err = s.AdoptLegacyFollowing(ctx); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) { /* Empty/unconfigured legacy credentials are left untouched. */
		}
	}
	list, err := s.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		m.clients[a.ID] = pixiv.NewClient(a.TokenFile, oauth, t)
		m.order = append(m.order, a.ID)
	}
	return m, nil
}
func (m *Manager) List(ctx context.Context) ([]domain.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list, err := m.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if c := m.clients[list[i].ID]; c != nil {
			list[i].Status = c.Status()
		}
	}
	return list, nil
}
func (m *Manager) Save(ctx context.Context, id, name, token string) (domain.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	if name == "" || len(name) > 120 || len(token) > 16384 || strings.ContainsAny(token, "\r\n") {
		return domain.Account{}, domain.ErrInvalid
	}
	var current domain.Account
	if id != "" {
		list, err := m.store.Accounts(ctx)
		if err != nil {
			return current, err
		}
		for _, a := range list {
			if a.ID == id {
				current = a
			}
		}
		if current.ID == "" {
			return current, domain.ErrNotFound
		}
	} else if token == "" {
		return current, domain.ErrInvalid
	}
	// An unverified imported identity must be repaired before adding another
	// account; otherwise the same Pixiv identity could be registered twice.
	list, err := m.store.Accounts(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	for _, a := range list {
		if a.PixivID == 0 && a.ID != id {
			return domain.Account{}, domain.ErrConflict
		}
	}
	if token == "" {
		current.Name = name
		if err := m.store.SaveAccount(ctx, current); err != nil {
			return domain.Account{}, err
		}
		current.Status = m.clients[id].Status()
		return current, nil
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return current, errors.New("credential directory unavailable")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return current, err
	}
	key := hex.EncodeToString(random[:])
	path := filepath.Join(m.dir, key+".token")
	if err := secret.Write(path, token); err != nil {
		return current, errors.New("credential save failed")
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	c := pixiv.NewClient(path, m.oauth, m.transport)
	uid, err := c.Authenticate(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	if current.PixivID > 0 && current.PixivID != uid {
		return domain.Account{}, domain.ErrConflict
	}
	if id == "" {
		id = key
	}
	a := domain.Account{ID: id, Name: name, PixivID: uid, TokenFile: path, Status: c.Status(), SyncState: "pending"}
	if err = m.store.SaveAccount(ctx, a); err != nil {
		return domain.Account{}, err
	}
	keep = true
	m.clients[id] = c
	if current.ID == "" {
		m.order = append(m.order, id)
	} else if current.TokenFile != path {
		_ = os.Remove(current.TokenFile)
	}
	// Return the persisted options; credential replacement never resets them.
	if saved, listErr := m.store.Accounts(ctx); listErr == nil {
		for _, account := range saved {
			if account.ID == a.ID {
				account.Status = c.Status()
				a = account
				break
			}
		}
	}
	return a, nil
}
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := m.store.Accounts(ctx)
	if err != nil {
		return err
	}
	var path string
	for _, a := range list {
		if a.ID == id {
			path = a.TokenFile
		}
	}
	if path == "" {
		return domain.ErrNotFound
	}
	if err = m.store.DeleteAccount(ctx, id); err != nil {
		return err
	}
	delete(m.clients, id)
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errors.New("credential removal failed")
	}
	return nil
}
func (m *Manager) Sync(ctx context.Context, id string) (string, error) {
	return m.store.SyncAccount(ctx, id)
}
func (m *Manager) client(ctx context.Context) (*pixiv.Client, error) {
	if id := domain.ContextAccount(ctx); id != "" {
		if c := m.clients[id]; c != nil {
			return c, nil
		}
		return nil, &pixiv.Error{Status: 401, Code: "ACCOUNT_REMOVED"}
	}
	for _, id := range m.order {
		if c := m.clients[id]; c != nil {
			return c, nil
		}
	}
	return nil, &pixiv.Error{Status: 401, Code: "ACCOUNT_NOT_CONFIGURED"}
}
func (m *Manager) Following(ctx context.Context, next string) (pixiv.FollowingPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(ctx)
	if e != nil {
		return pixiv.FollowingPage{}, e
	}
	page, err := c.Following(ctx, next)
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
func (m *Manager) ArtistArtworks(ctx context.Context, id int64, next string) (pixiv.ArtworkPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(ctx)
	if e != nil {
		return pixiv.ArtworkPage{}, e
	}
	return c.ArtistArtworks(ctx, id, next)
}
func (m *Manager) Artwork(ctx context.Context, id int64) (pixiv.RawArtwork, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(ctx)
	if e != nil {
		return pixiv.RawArtwork{}, e
	}
	return c.Artwork(ctx, id)
}
func (m *Manager) RelatedArtists(ctx context.Context, id int64) ([]pixiv.RawArtist, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(ctx)
	if e != nil {
		return nil, e
	}
	return c.RelatedArtists(ctx, id)
}
func (m *Manager) RelatedArtworks(ctx context.Context, id int64) ([]pixiv.RawArtwork, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(ctx)
	if e != nil {
		return nil, e
	}
	return c.RelatedArtworks(ctx, id)
}
func (m *Manager) Status() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, e := m.client(context.Background())
	if e != nil {
		return "expired"
	}
	return c.Status()
}
func (m *Manager) Credentials(ctx context.Context, token string) error {
	_, err := m.Save(ctx, "", "Pixiv 账号", token)
	return err
}
