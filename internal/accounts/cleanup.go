package accounts

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// ownedCredential accepts only manager-generated token files in its private directory.
// Legacy config paths and arbitrary database paths are never cleanup targets.
func ownedCredential(dir, path string) bool {
	root, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil || filepath.Dir(target) != root {
		return false
	}
	name := filepath.Base(target)
	if !strings.HasSuffix(name, ".token") {
		return false
	}
	key := strings.TrimSuffix(name, ".token")
	raw, err := hex.DecodeString(key)
	return err == nil && len(raw) == 16
}
func removeOwnedCredential(dir, path string) error {
	if !ownedCredential(dir, path) {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
