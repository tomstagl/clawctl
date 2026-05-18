// Package cache implements a simple TTL-bounded file cache used by
// `clawctl models` (and any future read-mostly endpoint we want to amortize
// across calls). It mirrors the bash _models_cache helper byte-for-byte:
// freshness is determined from the file's mtime, refresh failures fall back
// to the existing (stale) cache when one exists, and the cache file is
// written atomically via a sibling .tmp rename so concurrent readers never
// observe a half-written body.
package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FetchFunc returns the fresh body. Implementations are expected to be
// blocking (a network call) and to translate transport errors into the
// caller's typed error set; the cache layer treats every non-nil error as a
// refresh miss.
type FetchFunc func() ([]byte, error)

// Now is overridable from tests so the TTL boundary check is deterministic.
var Now = time.Now

// Get returns the cached body when it exists and is younger than ttl;
// otherwise it calls fetch, writes the result atomically, and returns it.
// When fetch fails and a cache file (even a stale one) exists, Get returns
// that stale body and a nil error so callers can degrade gracefully — this
// matches the bash helper's "fall back to stale cache" branch. When fetch
// fails AND no cache file exists, the fetch error is returned as-is.
//
// A ttl of zero forces a refresh on every call (useful for tests); a
// negative ttl is treated as zero.
func Get(path string, ttl time.Duration, fetch FetchFunc) ([]byte, error) {
	if path == "" {
		return nil, errors.New("cache: path required")
	}
	if fetch == nil {
		return nil, errors.New("cache: fetch func required")
	}
	if ttl < 0 {
		ttl = 0
	}

	if fresh, body, err := readIfFresh(path, ttl); err != nil {
		return nil, err
	} else if fresh {
		return body, nil
	}

	body, err := fetch()
	if err != nil {
		// Fall back to the stale cache if one exists. If it doesn't, the
		// caller gets the underlying transport error untouched so the
		// documented exit-code contract reaches the surface.
		if stale, readErr := os.ReadFile(path); readErr == nil {
			return stale, nil
		}
		return nil, err
	}

	if writeErr := writeAtomic(path, body); writeErr != nil {
		return nil, fmt.Errorf("cache: write %s: %w", path, writeErr)
	}
	return body, nil
}

// readIfFresh reports whether path exists and is younger than ttl. The
// body is returned only when fresh=true so callers don't need a second
// stat+read on the hot path.
func readIfFresh(path string, ttl time.Duration) (fresh bool, body []byte, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, err
	}
	if Now().Sub(info.ModTime()) > ttl {
		return false, nil, nil
	}
	body, err = os.ReadFile(path)
	if err != nil {
		return false, nil, err
	}
	return true, body, nil
}

// writeAtomic writes body to path via a same-directory tempfile + rename.
// Same-directory matters: cross-device renames on Linux/macOS would fail
// with EXDEV, so we never put the temp file in os.TempDir().
func writeAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
