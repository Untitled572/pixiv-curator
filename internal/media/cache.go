package media

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	previewCacheMagic            = "PVCACHE1"
	previewCacheTTL              = 7 * 24 * time.Hour
	previewCacheLimit      int64 = 256 << 20
	previewCacheEntryLimit int64 = 8 << 20
	previewCacheHeaderSize       = 8 + 2 + 8 + 8
)

type previewCacheEntry struct {
	path     string
	size     int64
	modified time.Time
	created  time.Time
}

type previewCacheValue struct {
	contentType string
	body        []byte
}

// PreviewCache stores bounded, validated thumbnails in a private directory.
// Cache filenames are hashes, so URL input never controls filesystem paths.
type PreviewCache struct {
	mu       sync.Mutex
	dir      string
	entries  map[string]previewCacheEntry
	total    int64
	now      func() time.Time
	ttl      time.Duration
	maxBytes int64
	maxEntry int64
}

func NewPreviewCache(dir string) (*PreviewCache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("empty preview cache directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	c := &PreviewCache{dir: dir, entries: make(map[string]previewCacheEntry), now: time.Now, ttl: previewCacheTTL, maxBytes: previewCacheLimit, maxEntry: previewCacheEntryLimit}
	if err := c.scan(); err != nil {
		return nil, err
	}
	return c, nil
}

func previewCacheKey(target string) string         { return fmt.Sprintf("%x", sha256.Sum256([]byte(target))) }
func (c *PreviewCache) filename(key string) string { return filepath.Join(c.dir, key+".cache") }

func allowedPreviewType(value string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return mediaType, true
	default:
		return "", false
	}
}

func isPreviewCacheKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (c *PreviewCache) scan() error {
	items, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	now := c.now()
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		name := item.Name()
		if strings.HasPrefix(name, ".preview-") && strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(c.dir, name))
			continue
		}
		if !strings.HasSuffix(name, ".cache") {
			continue
		}
		key := strings.TrimSuffix(name, ".cache")
		path := filepath.Join(c.dir, name)
		if !isPreviewCacheKey(key) {
			_ = os.Remove(path)
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		if info.Size() <= previewCacheHeaderSize || info.Size() > c.maxEntry+1024 {
			_ = os.Remove(path)
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		typ, size, created, parseErr := readPreviewHeader(f)
		_ = f.Close()
		if parseErr != nil || size != info.Size()-int64(previewCacheHeaderSize)-int64(len(typ)) || size < 0 || size > c.maxEntry || now.Sub(created) > c.ttl {
			_ = os.Remove(path)
			continue
		}
		if _, ok := allowedPreviewType(typ); !ok {
			_ = os.Remove(path)
			continue
		}
		c.entries[key] = previewCacheEntry{path: path, size: info.Size(), modified: info.ModTime(), created: created}
		c.total += info.Size()
	}
	c.evictLocked(0)
	if c.total > c.maxBytes {
		return errors.New("preview cache capacity could not be restored")
	}
	return nil
}

func readPreviewHeader(r io.Reader) (string, int64, time.Time, error) {
	fixed := make([]byte, previewCacheHeaderSize)
	if _, err := io.ReadFull(r, fixed); err != nil {
		return "", 0, time.Time{}, err
	}
	if string(fixed[:8]) != previewCacheMagic {
		return "", 0, time.Time{}, errors.New("invalid preview cache header")
	}
	typLen := binary.BigEndian.Uint16(fixed[8:10])
	if typLen == 0 || typLen > 64 {
		return "", 0, time.Time{}, errors.New("invalid preview cache type")
	}
	createdNanos := int64(binary.BigEndian.Uint64(fixed[10:18]))
	if createdNanos <= 0 {
		return "", 0, time.Time{}, errors.New("invalid preview cache time")
	}
	typ := make([]byte, typLen)
	if _, err := io.ReadFull(r, typ); err != nil {
		return "", 0, time.Time{}, err
	}
	size := binary.BigEndian.Uint64(fixed[18:26])
	if size > uint64(^uint64(0)>>1) {
		return "", 0, time.Time{}, errors.New("invalid preview cache size")
	}
	return string(typ), int64(size), time.Unix(0, createdNanos), nil
}

func (c *PreviewCache) get(key string) (previewCacheValue, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return previewCacheValue{}, false
	}
	if c.now().Sub(entry.created) > c.ttl {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	f, err := os.Open(entry.path)
	if err != nil {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	defer f.Close()
	typ, size, created, err := readPreviewHeader(f)
	if err != nil || size < 0 || size > c.maxEntry || !created.Equal(entry.created) || c.now().Sub(created) > c.ttl {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	canonical, valid := allowedPreviewType(typ)
	if !valid {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	body, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil || int64(len(body)) != size {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	if _, err := f.Read(make([]byte, 1)); err != io.EOF {
		c.removeLocked(key)
		return previewCacheValue{}, false
	}
	entry.modified = c.now()
	c.entries[key] = entry
	_ = os.Chtimes(entry.path, entry.modified, entry.modified)
	return previewCacheValue{contentType: canonical, body: body}, true
}

func (c *PreviewCache) head(key string) (string, int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", 0, false
	}
	if c.now().Sub(entry.created) > c.ttl {
		c.removeLocked(key)
		return "", 0, false
	}
	f, err := os.Open(entry.path)
	if err != nil {
		c.removeLocked(key)
		return "", 0, false
	}
	defer f.Close()
	typ, size, created, err := readPreviewHeader(f)
	if err != nil || size < 0 || size > c.maxEntry || !created.Equal(entry.created) || c.now().Sub(created) > c.ttl {
		c.removeLocked(key)
		return "", 0, false
	}
	info, err := f.Stat()
	if err != nil || info.Size() != int64(previewCacheHeaderSize)+int64(len(typ))+size {
		c.removeLocked(key)
		return "", 0, false
	}
	canonical, valid := allowedPreviewType(typ)
	if !valid {
		c.removeLocked(key)
		return "", 0, false
	}
	entry.modified = c.now()
	c.entries[key] = entry
	_ = os.Chtimes(entry.path, entry.modified, entry.modified)
	return canonical, size, true
}
func (c *PreviewCache) put(key, contentType string, body []byte) error {
	typ, ok := allowedPreviewType(contentType)
	if !ok || int64(len(body)) > c.maxEntry || len(body) == 0 {
		return errors.New("preview not cacheable")
	}
	entryBytes := int64(previewCacheHeaderSize+len(typ)) + int64(len(body))
	if entryBytes > c.maxBytes {
		return errors.New("preview exceeds cache capacity")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok {
		if c.now().Sub(existing.created) <= c.ttl {
			return nil
		}
		if !c.removeLocked(key) {
			return errors.New("cannot remove expired preview cache entry")
		}
	}
	tmp, err := os.CreateTemp(c.dir, ".preview-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	created := c.now()
	var header [previewCacheHeaderSize]byte
	copy(header[:8], previewCacheMagic)
	binary.BigEndian.PutUint16(header[8:10], uint16(len(typ)))
	binary.BigEndian.PutUint64(header[10:18], uint64(created.UnixNano()))
	binary.BigEndian.PutUint64(header[18:26], uint64(len(body)))
	if _, err = tmp.Write(header[:]); err == nil {
		_, err = tmp.Write([]byte(typ))
	}
	if err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	stat, err := os.Stat(tmpName)
	if err != nil {
		return err
	}
	c.evictLocked(stat.Size())
	if c.total+stat.Size() > c.maxBytes {
		return errors.New("preview cache capacity unavailable")
	}
	path := c.filename(key)
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	c.entries[key] = previewCacheEntry{path: path, size: info.Size(), modified: created, created: created}
	c.total += info.Size()
	return nil
}

func (c *PreviewCache) evictLocked(incoming int64) {
	if incoming > c.maxBytes {
		return
	}
	keys := make([]string, 0, len(c.entries))
	for key := range c.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return c.entries[keys[i]].modified.Before(c.entries[keys[j]].modified) })
	for _, key := range keys {
		if c.total+incoming <= c.maxBytes {
			break
		}
		c.removeLocked(key)
	}
}

func (c *PreviewCache) removeLocked(key string) bool {
	entry, ok := c.entries[key]
	if !ok {
		return true
	}
	if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	c.total -= entry.size
	delete(c.entries, key)
	return true
}

func (c *PreviewCache) removeKeys(keys map[string]bool, inverse bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	failed := false
	for key := range c.entries {
		if keys[key] != inverse && !c.removeLocked(key) {
			failed = true
		}
	}
	if failed {
		return errors.New("could not remove preview cache entries")
	}
	return nil
}
