package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

type loginProviderFunc struct {
	enabled bool
	login   func(context.Context) (string, error)
}

func (p loginProviderFunc) Enabled() bool                             { return p.enabled }
func (p loginProviderFunc) Login(ctx context.Context) (string, error) { return p.login(ctx) }

type credentialWriterFunc func(context.Context, string) error

func (f credentialWriterFunc) Credentials(ctx context.Context, token string) error {
	return f(ctx, token)
}

func waitAccountLogin(t *testing.T, service *AccountLogin, state string) AccountLoginStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if status.State == state {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("login did not reach %q; last status: %#v", state, service.Status())
	return AccountLoginStatus{}
}

func TestAccountLoginStartConflictAndSuccessHidesToken(t *testing.T) {
	started := make(chan struct{})
	continueLogin := make(chan struct{})
	var saved string
	service := NewAccountLogin(context.Background(), loginProviderFunc{enabled: true, login: func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-continueLogin:
			return "sensitive-refresh-token", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}, credentialWriterFunc(func(_ context.Context, token string) error { saved = token; return nil }))
	t.Cleanup(service.Close)
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := service.Start(); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Start error = %v; want conflict", err)
	}
	close(continueLogin)
	status := waitAccountLogin(t, service, "succeeded")
	if saved != "sensitive-refresh-token" {
		t.Fatal("credential writer did not receive provider token")
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{status.State, status.ErrorCode}, " ")), "token") {
		t.Fatalf("status contains credential material: %#v", status)
	}
}

func TestAccountLoginCancelAndTimeoutDoNotSave(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeout   time.Duration
		cancel    bool
		wantState string
		wantCode  string
	}{
		{name: "cancel", timeout: time.Second, cancel: true, wantState: "canceled"},
		{name: "timeout", timeout: 15 * time.Millisecond, wantState: "failed", wantCode: "LOGIN_TIMEOUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes int
			var mu sync.Mutex
			service := NewAccountLogin(context.Background(), loginProviderFunc{enabled: true, login: func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "must-not-be-saved", nil
			}}, credentialWriterFunc(func(context.Context, string) error { mu.Lock(); writes++; mu.Unlock(); return nil }))
			service.timeout = tc.timeout
			t.Cleanup(service.Close)
			if _, err := service.Start(); err != nil {
				t.Fatal(err)
			}
			if tc.cancel {
				service.Cancel()
			}
			status := waitAccountLogin(t, service, tc.wantState)
			if status.ErrorCode != tc.wantCode {
				t.Fatalf("error code = %q; want %q", status.ErrorCode, tc.wantCode)
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != 0 {
				t.Fatalf("credential writes = %d; want 0", writes)
			}
		})
	}
}

func TestAccountLoginSaveFailureIsSanitizedAndRateLimited(t *testing.T) {
	var calls int
	service := NewAccountLogin(context.Background(), loginProviderFunc{enabled: true, login: func(context.Context) (string, error) {
		calls++
		return "refresh-token", nil
	}}, credentialWriterFunc(func(context.Context, string) error { return errors.New("filesystem includes refresh-token") }))
	t.Cleanup(service.Close)
	for i := 0; i < 3; i++ {
		if _, err := service.Start(); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		status := waitAccountLogin(t, service, "failed")
		if status.ErrorCode != "CREDENTIAL_SAVE_FAILED" || strings.Contains(status.ErrorCode, "refresh-token") {
			t.Fatalf("unsanitized save failure status: %#v", status)
		}
	}
	if _, err := service.Start(); err == nil {
		t.Fatal("fourth Start succeeded; want rate limit")
	} else {
		var coded interface{ Error() string }
		if !errors.As(err, &coded) || coded.Error() != "LOGIN_RATE_LIMITED" {
			t.Fatalf("fourth Start error = %v; want LOGIN_RATE_LIMITED", err)
		}
	}
	if calls != 3 {
		t.Fatalf("provider calls = %d; want 3", calls)
	}
}

func TestAccountLoginRootContextExitRejectsRestart(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	service := NewAccountLogin(root, loginProviderFunc{enabled: true, login: func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }}, credentialWriterFunc(func(context.Context, string) error { return nil }))
	t.Cleanup(service.Close)
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	waitAccountLogin(t, service, "canceled")
	if _, err := service.Start(); err == nil {
		t.Fatal("Start after root context cancellation succeeded")
	}
}

type browserLoginProvider struct{ ready chan struct{} }

func (p browserLoginProvider) Enabled() bool                         { return true }
func (p browserLoginProvider) Login(context.Context) (string, error) { panic("automated login used") }
func (p browserLoginProvider) LoginBrowser(ctx context.Context, publish func(string), codes <-chan string) (string, error) {
	publish("https://app-api.pixiv.net/web/v1/login?code_challenge=test")
	close(p.ready)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-codes:
		return "fake-token", nil
	}
}
func TestBrowserCodeLifecycle(t *testing.T) {
	p := browserLoginProvider{make(chan struct{})}
	s := NewAccountLogin(context.Background(), p, credentialWriterFunc(func(context.Context, string) error { return nil }))
	defer s.Close()
	if s.SubmitCode("early") == nil {
		t.Fatal("accepted outside login")
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	<-p.ready
	if err := s.SubmitCode("one-time-code"); err != nil {
		t.Fatal(err)
	}
	if s.SubmitCode("duplicate") == nil {
		t.Fatal("accepted duplicate")
	}
	status := waitAccountLogin(t, s, "succeeded")
	if status.LoginURL != "" {
		t.Fatal("URL retained after completion")
	}
}
