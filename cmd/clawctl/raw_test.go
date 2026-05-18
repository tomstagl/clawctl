package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)


// stubTokenSource installs a fixed-token source for tests so runRaw doesn't
// shell out to the macOS Keychain. Callers MUST defer the returned restore
// closure to avoid leaking the override into other tests.
func stubTokenSource(t *testing.T) func() {
	t.Helper()
	prev := keychainTokenSource
	keychainTokenSource = func(config.Config) api.TokenSource {
		return func() (string, error) { return "tok-test", nil }
	}
	return func() { keychainTokenSource = prev }
}

func TestRunRaw_MissingHost(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	code := runRaw(context.Background(), config.Config{}, []string{"GET", "/health"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunRaw_DefaultsToGetHealth(t *testing.T) {
	defer stubTokenSource(t)()

	var seenMethod, seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenPath = r.URL.Path
		_, _ = w.Write([]byte("ok-default"))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runRaw(context.Background(), cfg, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if seenMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", seenMethod)
	}
	if seenPath != "/health" {
		t.Errorf("path = %s, want /health", seenPath)
	}
	if stdout.String() != "ok-default" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "ok-default")
	}
}

func TestRunRaw_GetSuccessSetsAuthAndTraceparent(t *testing.T) {
	defer stubTokenSource(t)()

	var seenAuth, seenTP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenTP = r.Header.Get("traceparent")
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runRaw(context.Background(), cfg, []string{"GET", "/v1/models"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if stdout.String() != `{"ok":1}` {
		t.Errorf("stdout = %q", stdout.String())
	}
	if seenAuth != "Bearer tok-test" {
		t.Errorf("Authorization = %q, want Bearer tok-test", seenAuth)
	}
	// traceparent shape is exercised in internal/trace; here we just confirm
	// it actually reached the wire and the trace-id was logged to stderr.
	if !strings.HasPrefix(seenTP, "00-") || !strings.HasSuffix(seenTP, "-01") {
		t.Errorf("traceparent = %q, want 00-...-01 shape", seenTP)
	}
	if !strings.Contains(stderr.String(), "trace-id: ") {
		t.Errorf("stderr = %q, want trace-id line", stderr.String())
	}
}

func TestRunRaw_PostSendsBodyAndDoesNotRetry(t *testing.T) {
	defer stubTokenSource(t)()

	var hits int32
	var seenBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(503) // even on 5xx, POST must not retry
		_, _ = w.Write([]byte("upstream"))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runRaw(context.Background(), cfg, []string{"POST", "/v1/echo", "-d", `{"x":1}`}, &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22 (HTTP 503)", code)
	}
	if string(seenBody) != `{"x":1}` {
		t.Errorf("body = %q, want {\"x\":1}", seenBody)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("POST 5xx retried: hits = %d, want 1", got)
	}
	if !strings.Contains(stdout.String(), "upstream") {
		t.Errorf("stdout = %q, want body forwarded on HTTP error", stdout.String())
	}
}

func TestRunRaw_HeaderFlagPropagates(t *testing.T) {
	defer stubTokenSource(t)()

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Custom")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runRaw(context.Background(), cfg, []string{"GET", "/h", "-H", "X-Custom: 42"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if seen != "42" {
		t.Errorf("X-Custom = %q, want 42", seen)
	}
}

func TestRunRaw_UnsupportedFlag(t *testing.T) {
	defer stubTokenSource(t)()
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: time.Second}
	code := runRaw(context.Background(), cfg, []string{"GET", "/x", "--user", "bob"}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unsupported flag") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
