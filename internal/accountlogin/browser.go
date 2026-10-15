package accountlogin

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// LoginBrowser uses gppt's PKCE implementation without controlling a browser.
// The private verifier remains in the child; only its challenge URL reaches the UI.
func (p Process) LoginBrowser(ctx context.Context, publish func(string), codes <-chan string) (string, error) {
	if !p.Enabled() {
		return "", &Error{"LOGIN_UNAVAILABLE"}
	}
	cmd := exec.CommandContext(ctx, p.Python, p.Helper, "browser-login")
	configureCancellation(cmd)
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", &Error{"LOGIN_FAILED"}
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", &Error{"LOGIN_FAILED"}
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return "", &Error{"LOGIN_FAILED"}
	}
	defer func() {
		in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Cancel()
			_ = cmd.Wait()
		}
	}()
	decoder := json.NewDecoder(io.LimitReader(out, 32768))
	var first struct {
		URL string `json:"login_url"`
	}
	if decoder.Decode(&first) != nil || !validLoginURL(first.URL) {
		return "", &Error{"LOGIN_FAILED"}
	}
	publish(first.URL)
	var result struct {
		Refresh string `json:"refresh_token"`
	}
	decoded := make(chan error, 1)
	go func() { decoded <- decoder.Decode(&result) }()
	select {
	case err = <-decoded:
	case <-ctx.Done():
		_ = cmd.Cancel()
		<-decoded
		return "", ctx.Err()
	case code := <-codes:
		if json.NewEncoder(in).Encode(map[string]string{"code": code}) != nil {
			_ = cmd.Cancel()
			<-decoded
			return "", &Error{"LOGIN_FAILED"}
		}
		in.Close()
		err = <-decoded
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil || waitErr != nil || result.Refresh == "" || len(result.Refresh) > 16384 || strings.ContainsAny(result.Refresh, " \r\n\t") {
		return "", &Error{"LOGIN_FAILED"}
	}
	return result.Refresh, nil
}

func validLoginURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "app-api.pixiv.net" && u.Host != "accounts.pixiv.net" || u.User != nil || u.Fragment != "" {
		return false
	}
	return (u.Path == "/web/v1/login" || u.Path == "/login") && u.Query().Get("code_challenge_method") == "S256" && u.Query().Get("code_challenge") != ""
}
