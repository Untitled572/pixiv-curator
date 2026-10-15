// Package network manages private, runtime-updatable outbound proxy settings.
package network

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/secret"
)

type Settings struct {
	Mode     string `json:"mode"`
	ProxyURL string `json:"proxy_url"`
}
type Status struct {
	Mode           string `json:"mode"`
	Address        string `json:"proxy_address"`
	HasCredentials bool   `json:"has_credentials"`
}
type Manager struct {
	mu       sync.RWMutex
	path     string
	settings Settings
}

func Open(path string) (*Manager, error) {
	m := &Manager{path: path, settings: Settings{Mode: "custom", ProxyURL: "http://127.0.0.1:7890"}}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return m, nil
	} else if err != nil {
		return nil, errors.New("network settings unavailable")
	}
	data, err := secret.ReadBytes(path)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(data, &m.settings) != nil || validate(m.settings) != nil {
		return nil, errors.New("invalid network settings")
	}
	return m, nil
}
func validate(s Settings) error {
	if s.Mode == "system" || s.Mode == "direct" {
		if s.ProxyURL != "" {
			return domain.ErrInvalid
		}
		return nil
	}
	if s.Mode != "custom" || len(s.ProxyURL) > 8192 || strings.ContainsAny(s.ProxyURL, " \t\r\n") {
		return domain.ErrInvalid
	}
	u, err := url.Parse(s.ProxyURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return domain.ErrInvalid
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return domain.ErrInvalid
		}
	}
	return nil
}
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := Status{Mode: m.settings.Mode}
	if m.settings.Mode == "custom" {
		u, _ := url.Parse(m.settings.ProxyURL)
		result.HasCredentials = u.User != nil
		u.User = nil
		result.Address = u.String()
	}
	return result
}
func (m *Manager) Save(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.ProxyURL = strings.TrimSpace(s.ProxyURL)
	if s.Mode == "custom" && s.ProxyURL == "" && m.settings.Mode == "custom" {
		s.ProxyURL = m.settings.ProxyURL
	}
	if s.Mode == "system" || s.Mode == "direct" {
		s.ProxyURL = ""
	}
	if err := validate(s); err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(m.path), 0700); err != nil {
		return errors.New("network settings save failed")
	}
	if err := secret.Write(m.path, string(data)); err != nil {
		return errors.New("network settings save failed")
	}
	m.settings = s
	return nil
}
func (m *Manager) Proxy(r *http.Request) (*url.URL, error) {
	m.mu.RLock()
	s := m.settings
	m.mu.RUnlock()
	switch s.Mode {
	case "direct":
		return nil, nil
	case "custom":
		return url.Parse(s.ProxyURL)
	default:
		return http.ProxyFromEnvironment(r)
	}
}

// Environment snapshots settings for a newly started OAuth helper; it never mutates
// the server's process environment or exposes credentials to HTTP responses.
func (m *Manager) Environment() []string {
	m.mu.RLock()
	s := m.settings
	m.mu.RUnlock()
	env := os.Environ()
	if s.Mode == "system" {
		return env
	}
	result := make([]string, 0, len(env)+2)
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		result = append(result, value)
	}
	if s.Mode == "custom" {
		result = append(result, "HTTP_PROXY="+s.ProxyURL, "HTTPS_PROXY="+s.ProxyURL)
	}
	return result
}
