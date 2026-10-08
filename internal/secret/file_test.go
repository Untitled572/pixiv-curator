package secret

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal("prepare private directory")
		}
	}
	return dir
}

func TestWriteUsesPrivateModesAndCanBeRead(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "refresh-token")
	const token = "token-value-123"
	if err := Write(path, token); err != nil {
		t.Fatalf("Write() failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() failed: %v", err)
	}
	if got != token {
		t.Fatal("Read() returned unexpected credential")
	}
	if runtime.GOOS != "windows" {
		fileInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal("stat credential file")
		}
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatal("stat credential directory")
		}
		if fileInfo.Mode().Perm() != 0600 {
			t.Fatalf("credential permissions = %04o, want 0600", fileInfo.Mode().Perm())
		}
		if dirInfo.Mode().Perm() != 0700 {
			t.Fatalf("directory permissions = %04o, want 0700", dirInfo.Mode().Perm())
		}
	}
}

func TestWriteAtomicallyReplacesExistingFile(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "refresh-token")
	if err := Write(path, "old-token"); err != nil {
		t.Fatalf("initial Write() failed: %v", err)
	}
	if err := Write(path, "new-token"); err != nil {
		t.Fatalf("replacement Write() failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() after replacement failed: %v", err)
	}
	if got != "new-token" {
		t.Fatal("replacement was not persisted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("read credential directory")
	}
	if len(entries) != 1 || entries[0].Name() != "refresh-token" {
		t.Fatal("temporary credential file remained after replacement")
	}
}

func TestReadRejectsEmptyMultilineAndOversizedFiles(t *testing.T) {
	dir := privateDir(t)
	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty"},
		{name: "multiline", data: []byte("first\nsecond\n")},
		{name: "oversized", data: []byte(strings.Repeat("x", 16385))},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name)
			if err := os.WriteFile(path, tt.data, 0600); err != nil {
				t.Fatal("prepare credential file")
			}
			if _, err := Read(path); err == nil {
				t.Fatal("Read() accepted invalid credential file")
			}
		})
	}
}

func TestWriteRejectsEmptyMultilineAndOversizedValues(t *testing.T) {
	dir := privateDir(t)
	cases := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "multiline", value: "first\nsecond"},
		{name: "oversized", value: strings.Repeat("x", 16385)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := Write(filepath.Join(dir, tt.name), tt.value); err == nil {
				t.Fatal("Write() accepted invalid credential")
			}
		})
	}
}

func TestPrivatePermissionsRequiredOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	parent := t.TempDir()
	dir := filepath.Join(parent, "exposed")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal("prepare exposed directory")
	}
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("token-value\n"), 0600); err != nil {
		t.Fatal("prepare credential file")
	}
	if _, err := Read(path); err == nil {
		t.Fatal("Read() accepted an exposed parent directory")
	}
	if err := Write(path, "replacement-token"); err == nil {
		t.Fatal("Write() accepted an exposed parent directory")
	}

	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal("make parent private")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal("make credential file exposed")
	}
	if _, err := Read(path); err == nil {
		t.Fatal("Read() accepted an exposed credential file")
	}
	if err := Write(path, "replacement-token"); err == nil {
		t.Fatal("Write() accepted an exposed credential file")
	}
}

func TestSymlinkIsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := privateDir(t)
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "credential")
	const token = "do-not-leak-this-token"
	if err := os.WriteFile(target, []byte(token+"\n"), 0600); err != nil {
		t.Fatal("prepare symlink target")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal("prepare credential symlink")
	}

	if _, err := Read(link); err == nil {
		t.Fatal("Read() followed a credential symlink")
	} else if strings.Contains(err.Error(), token) {
		t.Fatal("Read() exposed credential contents in its error")
	}
	if err := Write(link, token); err == nil {
		t.Fatal("Write() accepted a credential symlink")
	} else if strings.Contains(err.Error(), token) {
		t.Fatal("Write() exposed submitted credential in its error")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal("read symlink target")
	}
	if string(data) != token+"\n" {
		t.Fatal("Write() changed the symlink target")
	}
}

func TestWriteErrorsNeverIncludeSubmittedToken(t *testing.T) {
	const token = "sensitive-submitted-token"
	err := Write(filepath.Join(t.TempDir(), "missing", "credential"), token)
	if err == nil {
		t.Fatal("Write() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("Write() exposed submitted credential in its error")
	}
}
