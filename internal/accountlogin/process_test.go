package accountlogin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary substitutes a subprocess without Python or network dependencies.
func TestProcessHelper(t *testing.T) {
	if os.Getenv("CURATOR_TEST_HELPER") != "1" {
		return
	}
	switch os.Getenv("CURATOR_TEST_MODE") {
	case "success":
		fmt.Print(`{"refresh_token":"fake-refresh"}`)
	case "leak":
		fmt.Fprint(os.Stderr, "fake-private-stderr")
		fmt.Print(`{"error_code":"fake-private-response"}`)
		os.Exit(1)
	case "overflow":
		fmt.Print(strings.Repeat("x", 40000))
	case "wait":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}
func TestProcessBoundaries(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURATOR_TEST_HELPER", "1")
	for _, mode := range []string{"success", "leak", "overflow", "wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CURATOR_TEST_MODE", mode)
			timeout := 5 * time.Second
			if mode == "wait" {
				timeout = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			result, err := (Process{Python: executable, Helper: "-test.run=^TestProcessHelper$"}).Login(ctx)
			if mode == "success" {
				if err != nil || result != "fake-refresh" {
					t.Fatalf("result mismatch: %v", err)
				}
				return
			}
			if err == nil || result != "" {
				t.Fatal("invalid response accepted")
			}
			b, _ := json.Marshal(err.Error())
			if strings.Contains(string(b), "private") {
				t.Fatal("upstream error escaped")
			}
			if mode == "wait" && ctx.Err() == nil {
				t.Fatal("timeout did not cancel process")
			}
		})
	}
}
