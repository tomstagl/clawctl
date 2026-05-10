package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
)

func TestRunHealth_MissingHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHealth(context.Background(), config.Config{}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q, want CLAWCTL_HOST hint", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	got := stdout.String()
	want := "{\n  \"status\": \"ok\"\n}\n"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHealth_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22", code)
	}
	if !strings.Contains(stdout.String(), `"error": "not found"`) {
		t.Errorf("stdout = %q, want pretty body", stdout.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 404") {
		t.Errorf("stderr = %q, want HTTP 404 hint", stderr.String())
	}
}

// TestRunHealth_JSONLog asserts US-024: with CLAWCTL_LOG=json, runHealth
// emits exactly one structured JSON record on stderr in place of the
// human-friendly lines. Field set covers the success path.
func TestRunHealth_JSONLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, Log: "json"}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}

	out := strings.TrimRight(stderr.String(), "\n")
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("expected exactly one stderr line, got: %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v\nline=%q", err, out)
	}
	for _, k := range []string{"ts", "subcommand", "transport", "latency_ms", "exit_code", "redactions_count"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("required field %q missing: %v", k, rec)
		}
	}
	if got, want := rec["subcommand"], "health"; got != want {
		t.Errorf("subcommand = %v, want %v", got, want)
	}
	if got, want := rec["transport"], "api"; got != want {
		t.Errorf("transport = %v, want %v", got, want)
	}
	if got, want := rec["exit_code"], float64(0); got != want {
		t.Errorf("exit_code = %v, want %v", got, want)
	}
}

// TestRunHealth_JSONLog_FailedCall covers the failed-call shape:
// HTTP error → exit 22, no traceparent generated (health is anonymous),
// gateway-error stderr line suppressed in favour of the JSON record.
func TestRunHealth_JSONLog_FailedCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second, Log: "json"}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 22 {
		t.Fatalf("exit = %d, want 22", code)
	}

	out := strings.TrimRight(stderr.String(), "\n")
	if strings.Contains(out, "HTTP 503") {
		t.Errorf("human-friendly line leaked into stderr in JSON mode: %q", out)
	}
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("expected exactly one stderr line, got: %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("stderr is not JSON: %v\nline=%q", err, out)
	}
	if got, want := rec["exit_code"], float64(22); got != want {
		t.Errorf("exit_code = %v, want %v", got, want)
	}
	if _, ok := rec["traceparent"]; ok {
		t.Errorf("traceparent should be omitted (health is anonymous): %v", rec)
	}
}

func TestRunHealth_ConnRefused(t *testing.T) {
	// httptest server immediately closed → port refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: addr, Timeout: 1 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr = %q, want connection refused", stderr.String())
	}
}
