package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// stubInitCheck replaces both initCheckTokenSource and initCheckNewClient for
// the duration of the test, restoring them via t.Cleanup.
func stubInitCheck(t *testing.T, token string, tokenErr error, transport http.RoundTripper) {
	t.Helper()

	prevTok := initCheckTokenSource
	initCheckTokenSource = func(_ config.Config) api.TokenSource {
		return func() (string, error) { return token, tokenErr }
	}

	prevClient := initCheckNewClient
	initCheckNewClient = func(host string, timeout time.Duration) *api.Client {
		c := api.New(host, timeout, nil)
		c.Retries = 0
		c.HTTP = &http.Client{Transport: transport, Timeout: timeout}
		return c
	}

	t.Cleanup(func() {
		initCheckTokenSource = prevTok
		initCheckNewClient = prevClient
	})
}

// healthOKHandler returns a 200 /health response.
var healthOKHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
})

// health500Handler returns a 500 /health response.
var health500Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, `{"error":"internal"}`)
})

func TestInitCheck_AllPass(t *testing.T) {
	srv := httptest.NewServer(healthOKHandler)
	defer srv.Close()

	stubInitCheck(t, "tok", nil, nil) // nil transport → real HTTP to srv

	prevClient := initCheckNewClient
	initCheckNewClient = func(host string, timeout time.Duration) *api.Client {
		c := api.New(host, timeout, nil)
		c.Retries = 0
		return c
	}
	t.Cleanup(func() { initCheckNewClient = prevClient })

	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	var stdout bytes.Buffer
	code := runInitCheck(context.Background(), cfg, &stdout, io.Discard)
	if code != 0 {
		t.Errorf("exit = %d, want 0; stdout:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "All checks passed") {
		t.Errorf("stdout = %q, want 'All checks passed'", stdout.String())
	}
}

func TestInitCheck_HostUnset(t *testing.T) {
	var stdout bytes.Buffer
	code := runInitCheck(context.Background(), config.Config{}, &stdout, io.Discard)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "CLAWCTL_HOST") {
		t.Errorf("stdout = %q, want CLAWCTL_HOST mention", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("stdout = %q, want FAIL indicator", out)
	}
	if !strings.Contains(out, "SKIP") {
		t.Errorf("stdout = %q, want SKIP for token and health", out)
	}
}

func TestInitCheck_TokenFails(t *testing.T) {
	srv := httptest.NewServer(healthOKHandler)
	defer srv.Close()

	stubInitCheck(t, "", errors.New("keychain: item not found"), nil)

	prevClient := initCheckNewClient
	initCheckNewClient = func(host string, timeout time.Duration) *api.Client {
		c := api.New(host, timeout, nil)
		c.Retries = 0
		return c
	}
	t.Cleanup(func() { initCheckNewClient = prevClient })

	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	var stdout bytes.Buffer
	code := runInitCheck(context.Background(), cfg, &stdout, io.Discard)
	if code != 2 {
		t.Errorf("exit = %d, want 2; stdout:\n%s", code, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("stdout = %q, want FAIL for token check", out)
	}
	if !strings.Contains(out, "keychain") {
		t.Errorf("stdout = %q, want keychain error text", out)
	}
	// Health check still runs (unauthenticated) and passes.
	if !strings.Contains(out, checkPass+" health") {
		t.Errorf("stdout = %q, want health to still pass", out)
	}
}

func TestInitCheck_HealthFails(t *testing.T) {
	srv := httptest.NewServer(health500Handler)
	defer srv.Close()

	stubInitCheck(t, "tok", nil, nil)

	prevClient := initCheckNewClient
	initCheckNewClient = func(host string, timeout time.Duration) *api.Client {
		c := api.New(host, timeout, nil)
		c.Retries = 0
		return c
	}
	t.Cleanup(func() { initCheckNewClient = prevClient })

	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	var stdout bytes.Buffer
	code := runInitCheck(context.Background(), cfg, &stdout, io.Discard)
	if code != 2 {
		t.Errorf("exit = %d, want 2; stdout:\n%s", code, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "FAIL") {
		t.Errorf("stdout = %q, want FAIL for health check", out)
	}
	if !strings.Contains(out, "HTTP 500") {
		t.Errorf("stdout = %q, want HTTP 500 in health failure", out)
	}
}
