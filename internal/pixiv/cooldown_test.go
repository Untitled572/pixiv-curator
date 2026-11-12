package pixiv

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPICooldownDoesNotCallUpstreamAndRecovers(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return response(429, "", http.Header{"Retry-After": []string{"17"}}), nil
		}
		return response(200, `{"ok":true}`, nil), nil
	}))
	c.access = "fake-access"
	c.expires = time.Now().Add(time.Hour)
	var payload map[string]any
	for i := 0; i < 2; i++ {
		err := c.get(context.Background(), apiOrigin+"/v1/test", &payload)
		var limited *Error
		if !errors.As(err, &limited) || limited.Code != "RATE_LIMITED" || limited.LocalCooldown != (i == 1) || limited.RetryAfter <= 0 {
			t.Fatalf("call %d: %+v", i, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls during cooldown=%d", calls.Load())
	}
	c.mu.Lock()
	c.cooldown = time.Now().Add(-time.Second)
	c.mu.Unlock()
	c.nextRequest = time.Time{}
	if err := c.get(context.Background(), apiOrigin+"/v1/test", &payload); err != nil || calls.Load() != 2 || payload["ok"] != true {
		t.Fatalf("recovery err=%v calls=%d payload=%v", err, calls.Load(), payload)
	}
}
