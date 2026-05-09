package main

import (
	"bytes"
	"context"
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
