package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// withStubTokenSource swaps in a fixed token for the test so runModels never
// touches the real Keychain.
func withStubTokenSource(t *testing.T, token string) {
	t.Helper()
	prev := keychainTokenSource
	keychainTokenSource = func(_ config.Config) api.TokenSource {
		return func() (string, error) { return token, nil }
	}
	t.Cleanup(func() { keychainTokenSource = prev })
}

func TestRunModels_MissingHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runModels(context.Background(), config.Config{CacheDir: t.TempDir()}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunModels_Success_PrettyPrintsAndCaches(t *testing.T) {
	withStubTokenSource(t, "tok-abc")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-abc" {
			t.Errorf("auth = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"openclaw/example"}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        dir,
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}

	var stdout, stderr bytes.Buffer
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	wantOut := `{
  "data": [
    {
      "id": "openclaw/example"
    }
  ]
}
`
	if stdout.String() != wantOut {
		t.Errorf("stdout = %q, want %q", stdout.String(), wantOut)
	}

	// Cache file should have been persisted with the raw response body.
	persisted, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatalf("cache file: %v", err)
	}
	if string(persisted) != `{"data":[{"id":"openclaw/example"}]}` {
		t.Errorf("cache body = %q", persisted)
	}

	// Second call should hit the cache, not the server.
	stdout.Reset()
	stderr.Reset()
	code = runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hit %d times across 2 calls, want 1 (cache miss + cache hit)", got)
	}
	if stdout.String() != wantOut {
		t.Errorf("cached stdout = %q, want %q", stdout.String(), wantOut)
	}
}

func TestRunModels_HTTPError_BodyToStdoutExit22(t *testing.T) {
	withStubTokenSource(t, "tok")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22", code)
	}
	if !strings.Contains(stdout.String(), `"unauthorized"`) {
		t.Errorf("stdout = %q, want pretty body", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 401") {
		t.Errorf("stderr = %q, want HTTP 401", stderr.String())
	}
}

func TestRunModels_FetchFailure_FallsBackToStaleCache(t *testing.T) {
	withStubTokenSource(t, "tok")

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(cachePath, []byte(`{"data":[{"id":"openclaw/cached"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the cache to look stale.
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cachePath, stale, stale); err != nil {
		t.Fatal(err)
	}

	// Server is closed before the call → ECONNREFUSED. With a stale cache
	// available the call should still succeed (exit 0) with the cached body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	cfg := config.Config{
		Host:            addr,
		CacheDir:        dir,
		KeychainService: "test",
		Timeout:         time.Second,
		ModelsTTL:       60 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s), want 0 with stale cache fallback", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"openclaw/cached"`) {
		t.Errorf("stdout = %q, want stale cache body", stdout.String())
	}
}

func TestRunModels_FetchFailure_NoCachePropagatesExitCode(t *testing.T) {
	withStubTokenSource(t, "tok")

	// No cache, server unreachable → exit 7.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	cfg := config.Config{
		Host:            addr,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         time.Second,
		ModelsTTL:       60 * time.Second,
	}
	var stdout, stderr bytes.Buffer
	code := runModels(context.Background(), cfg, &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
