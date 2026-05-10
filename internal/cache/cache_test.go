package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withFrozenClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := Now
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = prev })
}

func TestGet_MissReadsFromFetcherAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	called := 0
	body, err := Get(path, time.Minute, func() ([]byte, error) {
		called++
		return []byte(`{"data":[]}`), nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"data":[]}` {
		t.Errorf("body = %q", body)
	}
	if called != 1 {
		t.Errorf("fetch called %d times, want 1", called)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cache file not persisted: %v", err)
	}
	if string(persisted) != `{"data":[]}` {
		t.Errorf("persisted = %q", persisted)
	}
}

func TestGet_FreshHitSkipsFetcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"cached":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	withFrozenClock(t, now.Add(30*time.Second))

	body, err := Get(path, 60*time.Second, func() ([]byte, error) {
		t.Fatal("fetch should not be called on a fresh hit")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"cached":true}` {
		t.Errorf("body = %q", body)
	}
}

func TestGet_TTLBoundary(t *testing.T) {
	// Bash uses `age_sec > MODELS_TTL` — so age == TTL is still fresh, age == TTL+1 is stale.
	// Mirror that here: ttl = 60s, we test at 60s (fresh) and at 60.000001s (stale).
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`stale-or-fresh`), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	t.Run("equal-to-ttl is fresh", func(t *testing.T) {
		withFrozenClock(t, mtime.Add(60*time.Second))
		body, err := Get(path, 60*time.Second, func() ([]byte, error) {
			t.Fatal("fetch should not run when age == ttl")
			return nil, nil
		})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(body) != "stale-or-fresh" {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("just-over-ttl triggers refresh", func(t *testing.T) {
		withFrozenClock(t, mtime.Add(60*time.Second+time.Nanosecond))
		called := false
		body, err := Get(path, 60*time.Second, func() ([]byte, error) {
			called = true
			return []byte(`refreshed`), nil
		})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !called {
			t.Errorf("fetch not called when age > ttl")
		}
		if string(body) != "refreshed" {
			t.Errorf("body = %q, want refreshed", body)
		}
	})
}

func TestGet_FetchFailure_FallsBackToStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	want := errors.New("network is down")
	body, err := Get(path, time.Minute, func() ([]byte, error) { return nil, want })
	if err != nil {
		t.Fatalf("Get returned %v, want stale-cache fallback", err)
	}
	if string(body) != `{"stale":true}` {
		t.Errorf("body = %q, want stale fallback", body)
	}
}

func TestGet_FetchFailure_NoCachePropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	want := errors.New("connection refused")
	_, err := Get(path, time.Minute, func() ([]byte, error) { return nil, want })
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestGet_AtomicWrite_NoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	if _, err := Get(path, time.Minute, func() ([]byte, error) {
		return []byte(`{"ok":1}`), nil
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "models.json" {
			continue
		}
		t.Errorf("unexpected leftover in cache dir: %s", e.Name())
	}
}

func TestGet_PathRequired(t *testing.T) {
	if _, err := Get("", time.Minute, func() ([]byte, error) { return nil, nil }); err == nil {
		t.Errorf("expected error for empty path")
	}
}

func TestGet_FetchRequired(t *testing.T) {
	if _, err := Get(filepath.Join(t.TempDir(), "x"), time.Minute, nil); err == nil {
		t.Errorf("expected error for nil fetch")
	}
}
