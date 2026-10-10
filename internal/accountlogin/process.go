// Package accountlogin adapts an optional local gppt browser login process.
package accountlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

type Process struct{ Python, Helper string }

func (p Process) Enabled() bool { return p.Python != "" && p.Helper != "" }

// Login runs only a configured executable; HTTP input never selects a command.
func (p Process) Login(ctx context.Context) (string, error) {
	if !p.Enabled() {
		return "", &Error{"LOGIN_UNAVAILABLE"}
	}
	dir, err := os.MkdirTemp("", "curator-login-*")
	if err != nil {
		return "", &Error{"LOGIN_FAILED"}
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, p.Python, p.Helper, "login")
	cmd.Env = append(os.Environ(), "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir, "PYTHONUNBUFFERED=1")
	configureCancellation(cmd)
	cmd.WaitDelay = 3 * time.Second
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var result struct {
		Refresh string `json:"refresh_token"`
		Code    string `json:"error_code"`
	}
	if out.overflow || json.Unmarshal(out.Bytes(), &result) != nil {
		return "", &Error{"LOGIN_FAILED"}
	}
	if runErr != nil || result.Code != "" {
		switch result.Code {
		case "LOGIN_DEPENDENCY_UNAVAILABLE", "LOGIN_BROWSER_UNAVAILABLE", "LOGIN_DISPLAY_UNAVAILABLE":
			return "", &Error{result.Code}
		default:
			return "", &Error{"LOGIN_FAILED"}
		}
	}
	if result.Refresh == "" || len(result.Refresh) > 16384 || strings.ContainsAny(result.Refresh, " \r\n\t") {
		return "", &Error{"LOGIN_FAILED"}
	}
	return result.Refresh, nil
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 32768 {
		b.overflow = true
		return 0, errors.New("login output exceeded limit")
	}
	return b.Buffer.Write(p)
}
