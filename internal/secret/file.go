// Package secret handles private local credential files. Errors never include contents.
package secret

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrFile = errors.New("credential file must be a private regular file with a nonempty token")

func Read(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", ErrFile
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return "", ErrFile
	}
	if !st.Mode().IsRegular() || st.Size() > 16384 || runtime.GOOS != "windows" && (st.Mode().Perm()&0077 != 0 || dir.Mode().Perm()&0077 != 0) {
		return "", ErrFile
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrFile
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) || !opened.Mode().IsRegular() || runtime.GOOS != "windows" && opened.Mode().Perm()&0077 != 0 {
		return "", ErrFile
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return "", ErrFile
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return "", ErrFile
	}
	return value, nil
}

// Write replaces a credential atomically; failed persistence is never reported as success.
func Write(path, value string) error {
	if value == "" || len(value) > 16384 || strings.ContainsAny(value, "\r\n\t ") {
		return ErrFile
	}
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0 {
		return ErrFile
	}
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0) {
		return ErrFile
	} else if err != nil && !os.IsNotExist(err) {
		return ErrFile
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return ErrFile
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.WriteString(value + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrFile
	}
	if err = os.Rename(name, path); err != nil {
		return ErrFile
	}
	if runtime.GOOS != "windows" {
		d, err := os.Open(dir)
		if err != nil {
			return ErrFile
		}
		defer d.Close()
		if err = d.Sync(); err != nil {
			return ErrFile
		}
	}
	return nil
}
