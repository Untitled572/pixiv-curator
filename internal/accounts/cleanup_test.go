package accounts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialCleanupStaysWithinManagedDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "00112233445566778899aabbccddeeff.token")
	inside := filepath.Join(dir, "00112233445566778899aabbccddeeff.token")
	legacy := filepath.Join(dir, "pixiv-refresh-token")
	for _, p := range []string{outside, inside, legacy} {
		if e := os.WriteFile(p, []byte("fake"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []string{outside, inside, legacy} {
		if e := removeOwnedCredential(dir, p); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := os.Stat(inside); !os.IsNotExist(e) {
		t.Fatal("managed token retained")
	}
	for _, p := range []string{outside, legacy} {
		if _, e := os.Stat(p); e != nil {
			t.Fatalf("unowned token affected: %v", e)
		}
	}
}
