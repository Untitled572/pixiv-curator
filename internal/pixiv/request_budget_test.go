package pixiv

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestAdditionalBudgetStops401Recovery(t *testing.T) {
	exhausted := &Error{Code: "REQUEST_BUDGET_EXHAUSTED"}
	calls := 0
	c := testClient(t, testTokenFile(t), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != apiOrigin+"/test" {
			t.Fatal("unexpected request")
		}
		return response(401, `{}`, nil), nil
	}))
	c.access, c.expires, c.state = "old", time.Now().Add(time.Hour), "ready"
	ctx := WithAdditionalRequestBudget(context.Background(), func(context.Context) error { return exhausted })
	err := c.get(ctx, apiOrigin+"/test", new(any))
	if err != exhausted || calls != 1 || c.Status() != "ready" || c.access != "old" {
		t.Fatalf("error=%v calls=%d state=%s", err, calls, c.Status())
	}
}

func TestAdditionalBudgetRefreshAndRetry(t *testing.T) {
	for _, rejectRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "allow", true: "reject_retry"}[rejectRetry], func(t *testing.T) {
			calls, reserves := 0, 0
			denied := errors.New("denied")
			c := testClient(t, testTokenFile(t), roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() == oauthURL {
					return oauthResponse("new", ""), nil
				}
				if calls == 1 {
					return response(401, `{}`, nil), nil
				}
				return response(200, `{}`, nil), nil
			}))
			c.access, c.expires, c.state = "old", time.Now().Add(time.Hour), "ready"
			ctx := WithAdditionalRequestBudget(context.Background(), func(context.Context) error {
				reserves++
				if rejectRetry && reserves == 2 {
					return denied
				}
				return nil
			})
			err := c.get(ctx, apiOrigin+"/test", new(any))
			if rejectRetry {
				if err != denied || calls != 2 || reserves != 2 {
					t.Fatalf("error=%v calls=%d reserves=%d", err, calls, reserves)
				}
			} else if err != nil || calls != 3 || reserves != 2 {
				t.Fatalf("error=%v calls=%d reserves=%d", err, calls, reserves)
			}
		})
	}
}

func TestAdditionalBudgetRejectsRefreshWithoutNetwork(t *testing.T) {
	for _, denied := range []error{context.Canceled, errors.New("lease lost")} {
		t.Run(denied.Error(), func(t *testing.T) {
			calls := 0
			c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return oauthResponse("new", ""), nil }))
			ctx := WithAdditionalRequestBudget(context.Background(), func(context.Context) error { return denied })
			_, err := c.accessToken(ctx, "")
			if err != denied || calls != 0 || c.Status() != "configured" {
				t.Fatalf("error=%v calls=%d state=%s", err, calls, c.Status())
			}
		})
	}
}

func TestAdditionalBudgetOptionalRefresh(t *testing.T) {
	calls := 0
	c := testClient(t, testTokenFile(t), roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return oauthResponse("new", ""), nil }))
	token, err := c.accessToken(context.Background(), "")
	if err != nil || token != "new" || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
