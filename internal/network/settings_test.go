package network

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPersistenceRedactionAndRuntimeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "network.json")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "https://app-api.pixiv.net/", nil)
	custom := Settings{Mode: "custom", ProxyURL: "http://test-user:test-password@127.0.0.1:7890"}
	if err = m.Save(custom); err != nil {
		t.Fatal(err)
	}
	status, _ := json.Marshal(m.Status())
	if strings.Contains(string(status), "test-user") || strings.Contains(string(status), "test-password") {
		t.Fatal("credentials leaked")
	}
	if !m.Status().HasCredentials || m.Status().Address != "http://127.0.0.1:7890" {
		t.Fatal("incorrect safe status")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := reopened.Proxy(req)
	if err != nil || proxy.String() != custom.ProxyURL {
		t.Fatal("saved proxy not restored")
	}
	if err = m.Save(Settings{Mode: "custom"}); err != nil {
		t.Fatal(err)
	}
	if err = m.Save(Settings{Mode: "custom", ProxyURL: "http://invalid:99999"}); err == nil {
		t.Fatal("invalid port accepted")
	}
	if m.Status().Address != "http://127.0.0.1:7890" {
		t.Fatal("invalid save changed current settings")
	}
	if err = m.Save(Settings{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if proxy, err = m.Proxy(req); err != nil || proxy != nil {
		t.Fatal("direct still uses proxy")
	}
	t.Setenv("HTTP_PROXY", "http://old-proxy:8080")
	for _, v := range m.Environment() {
		if strings.HasPrefix(strings.ToLower(v), "http_proxy=") {
			t.Fatal("direct helper inherits proxy")
		}
	}
}
func TestSaveFailureDoesNotChangeActiveProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "network.json")
	m, _ := Open(path)
	if err := m.Save(Settings{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if m.Save(Settings{Mode: "custom", ProxyURL: "http://localhost:7890"}) == nil {
		t.Fatal("invalid file accepted")
	}
	if m.Status().Mode != "direct" {
		t.Fatal("failure changed memory state")
	}
}
func TestConcurrentNetworkSettings(t *testing.T) {
	m, _ := Open(filepath.Join(t.TempDir(), "private", "network.json"))
	req, _ := http.NewRequest("GET", "https://i.pximg.net/", nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				m.Status()
				m.Proxy(req)
				m.Environment()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if err := m.Save(Settings{Mode: "direct"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
