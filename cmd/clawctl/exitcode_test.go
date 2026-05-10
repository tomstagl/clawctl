package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// roundTripFunc lets tests implement http.RoundTripper with a plain function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubHealthClient overrides newHealthAPIClient for the duration of the test.
// The returned client has Retries=0 so mock transport errors are returned
// immediately without the 1s-per-retry delay that the production client uses.
func stubHealthClient(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	prev := newHealthAPIClient
	newHealthAPIClient = func(host string, timeout time.Duration) *api.Client {
		c := api.New(host, timeout, nil)
		c.Retries = 0
		c.HTTP = &http.Client{Transport: transport, Timeout: timeout}
		return c
	}
	t.Cleanup(func() { newHealthAPIClient = prev })
}

// TestExitCode_UsageError verifies exit code 2 when CLAWCTL_HOST is unset.
func TestExitCode_UsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHealth(context.Background(), config.Config{}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST") {
		t.Errorf("stderr = %q, want CLAWCTL_HOST hint", stderr.String())
	}
}

// TestExitCode_DNSFailure verifies exit code 6 when the DNS lookup fails.
// Uses a mock transport that returns a net.DNSError so the test is fast and
// deterministic without relying on the .invalid TLD or real DNS.
func TestExitCode_DNSFailure(t *testing.T) {
	stubHealthClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &net.DNSError{
			Err:        "no such host",
			Name:       "clawctl-test-dns.invalid",
			IsNotFound: true,
		}
	}))

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://clawctl-test-dns.invalid:18789", Timeout: 2 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 6 {
		t.Errorf("exit = %d, want 6 (DNS failure); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "DNS") {
		t.Errorf("stderr = %q, want DNS hint", stderr.String())
	}
}

// TestExitCode_ConnRefused verifies exit code 7 when the TCP connection is refused.
// Uses a mock transport that returns ECONNREFUSED so no server is needed.
func TestExitCode_ConnRefused(t *testing.T) {
	stubHealthClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: syscall.ECONNREFUSED,
		}
	}))

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: 2 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7 (connection refused); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr = %q, want connection refused hint", stderr.String())
	}
}

// TestExitCode_HTTPError verifies exit code 22 for HTTP 4xx/5xx responses.
// Uses a real httptest server returning 401.
func TestExitCode_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: srv.URL, Timeout: 2 * time.Second}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 22 {
		t.Errorf("exit = %d, want 22 (HTTP 4xx); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "HTTP 401") {
		t.Errorf("stderr = %q, want HTTP 401 hint", stderr.String())
	}
}

// TestExitCode_Timeout verifies exit code 28 when the request times out.
// Uses a mock transport that returns a context.DeadlineExceeded-style error
// so the test is fast and does not require a sleeping server.
func TestExitCode_Timeout(t *testing.T) {
	stubHealthClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &timeoutErr{errors.New("i/o timeout")}
	}))

	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}
	code := runHealth(context.Background(), cfg, &stdout, &stderr)
	if code != 28 {
		t.Errorf("exit = %d, want 28 (timeout); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "timeout") {
		t.Errorf("stderr = %q, want timeout hint", stderr.String())
	}
}

// timeoutErr is a net.Error that reports Timeout() == true, matching what
// net/http returns for dial timeouts and context deadline exceeded.
type timeoutErr struct{ err error }

func (e *timeoutErr) Error() string   { return e.err.Error() }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }
