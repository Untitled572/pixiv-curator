package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/accountlogin"
	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type AccountLoginProvider interface {
	Enabled() bool
	Login(context.Context) (string, error)
}
type AccountCredentialWriter interface {
	Credentials(context.Context, string) error
}

type AccountLoginStatus struct {
	Enabled    bool   `json:"enabled"`
	State      string `json:"state"`
	ErrorCode  string `json:"error_code,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// AccountLogin runs a bounded, non-persistent local login. Passwords stay in Pixiv's window.
type AccountLogin struct {
	mu          sync.Mutex
	root        context.Context
	provider    AccountLoginProvider
	credentials AccountCredentialWriter
	status      AccountLoginStatus
	cancel      context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
	attempts    int
	window      time.Time
	timeout     time.Duration
}

func NewAccountLogin(ctx context.Context, p AccountLoginProvider, c AccountCredentialWriter) *AccountLogin {
	return &AccountLogin{root: ctx, provider: p, credentials: c, status: AccountLoginStatus{Enabled: p.Enabled(), State: "idle"}, timeout: 6 * time.Minute}
}
func (s *AccountLogin) Status() AccountLoginStatus { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *AccountLogin) Start() (AccountLoginStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.root.Err() != nil || !s.status.Enabled {
		return s.status, &accountlogin.Error{Code: "LOGIN_UNAVAILABLE"}
	}
	if s.status.State == "running" {
		return s.status, domain.ErrConflict
	}
	now := time.Now()
	if now.Sub(s.window) >= time.Minute {
		s.window = now
		s.attempts = 0
	}
	if s.attempts >= 3 {
		return s.status, &accountlogin.Error{Code: "LOGIN_RATE_LIMITED"}
	}
	s.attempts++
	ctx, cancel := context.WithTimeout(s.root, s.timeout)
	s.cancel = cancel
	s.status = AccountLoginStatus{Enabled: true, State: "running", StartedAt: now.UnixMilli()}
	s.wg.Add(1)
	go s.run(ctx, cancel)
	return s.status, nil
}
func (s *AccountLogin) run(ctx context.Context, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	token, err := s.provider.Login(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = s.credentials.Credentials(ctx, token)
		if err != nil {
			err = &accountlogin.Error{Code: "CREDENTIAL_SAVE_FAILED"}
		}
	}
	token = ""
	s.status.FinishedAt = time.Now().UnixMilli()
	s.cancel = nil
	switch {
	case errors.Is(err, context.Canceled):
		s.status.State = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		s.status.State = "failed"
		s.status.ErrorCode = "LOGIN_TIMEOUT"
	case err != nil:
		s.status.State = "failed"
		s.status.ErrorCode = "LOGIN_FAILED"
		var known *accountlogin.Error
		if errors.As(err, &known) {
			switch known.Code {
			case "LOGIN_DEPENDENCY_UNAVAILABLE", "LOGIN_BROWSER_UNAVAILABLE", "LOGIN_DISPLAY_UNAVAILABLE", "CREDENTIAL_SAVE_FAILED":
				s.status.ErrorCode = known.Code
			}
		}
	default:
		s.status.State = "succeeded"
	}
}
func (s *AccountLogin) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}
func (s *AccountLogin) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
