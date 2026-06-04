package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGet_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header")
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unauthenticated request leaked Authorization: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, nil)
	body, err := c.Get(context.Background(), "/health", false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"status":"ok"}` {
		t.Errorf("body = %q", body)
	}
}

func TestGet_AuthedSendsBearer(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, func() (string, error) { return "tok-123", nil })
	if _, err := c.Get(context.Background(), "/v1/models", true); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if seen != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", seen)
	}
}

func TestGet_TokenSourceFailure(t *testing.T) {
	c := New("http://example.invalid", time.Second, func() (string, error) {
		return "", errors.New("keychain: item not found")
	})
	_, err := c.Get(context.Background(), "/v1/models", true)
	if err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("err = %v, want keychain failure", err)
	}
}

func TestGet_HTTPErrorNoRetryFor4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second, nil)
	c.RetryDelay = time.Millisecond
	_, err := c.Get(context.Background(), "/missing", false)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *HTTPError", err)
	}
	if httpErr.StatusCode != 404 {
		t.Errorf("status = %d, want 404", httpErr.StatusCode)
	}
	if string(httpErr.Body) != `{"error":"not found"}` {
		t.Errorf("body = %q", httpErr.Body)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("4xx retried: %d hits, want 1", got)
	}
	if ExitCode(err) != 22 {
		t.Errorf("ExitCode = %d, want 22", ExitCode(err))
	}
}

func TestGet_HTTPError5xxRetriesThenFails(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`upstream`))
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second, nil)
	c.RetryDelay = time.Millisecond
	_, err := c.Get(context.Background(), "/x", false)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *HTTPError", err)
	}
	if httpErr.StatusCode != 503 {
		t.Errorf("status = %d, want 503", httpErr.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("5xx retried %d times, want 3 attempts", got)
	}
}

func TestGet_HTTPError5xxRecoversAfterRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 2 {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`oops`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second, nil)
	c.RetryDelay = time.Millisecond
	body, err := c.Get(context.Background(), "/x", false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != `{"ok":1}` {
		t.Errorf("body = %q", body)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestGet_ConnRefused(t *testing.T) {
	// Bind a port, immediately close — ECONNREFUSED on subsequent dial.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	c := New("http://"+addr, time.Second, nil)
	c.RetryDelay = time.Millisecond
	_, err = c.Get(context.Background(), "/health", false)
	var refErr *ConnRefusedError
	if !errors.As(err, &refErr) {
		t.Fatalf("err = %v, want *ConnRefusedError", err)
	}
	if ExitCode(err) != 7 {
		t.Errorf("ExitCode = %d, want 7", ExitCode(err))
	}
}

func TestGet_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte(`late`))
	}))
	defer srv.Close()

	c := New(srv.URL, 50*time.Millisecond, nil)
	c.RetryDelay = time.Millisecond
	_, err := c.Get(context.Background(), "/", false)
	var toErr *TimeoutError
	if !errors.As(err, &toErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if ExitCode(err) != 28 {
		t.Errorf("ExitCode = %d, want 28", ExitCode(err))
	}
}

func TestGet_DNSFailure(t *testing.T) {
	c := New("http://nonexistent.invalid.localdomain.example", 2*time.Second, nil)
	c.RetryDelay = time.Millisecond
	_, err := c.Get(context.Background(), "/", false)
	var dnsErr *DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("err = %v, want *DNSError", err)
	}
	if ExitCode(err) != 6 {
		t.Errorf("ExitCode = %d, want 6", ExitCode(err))
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{&DNSError{Host: "x"}, 6},
		{&ConnRefusedError{Host: "x"}, 7},
		{&HTTPError{StatusCode: 500}, 22},
		{&ResponseTooLargeError{Limit: 1024}, 22},
		{&TimeoutError{Host: "x", Timeout: time.Second}, 28},
		{fmt.Errorf("unknown"), 1},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestReadLimited(t *testing.T) {
	t.Run("under limit", func(t *testing.T) {
		b, err := ReadLimited(strings.NewReader("hello"), 10)
		if err != nil {
			t.Fatalf("ReadLimited: %v", err)
		}
		if string(b) != "hello" {
			t.Errorf("body = %q", b)
		}
	})
	t.Run("exactly at limit", func(t *testing.T) {
		b, err := ReadLimited(strings.NewReader("hello"), 5)
		if err != nil {
			t.Fatalf("ReadLimited at boundary: %v", err)
		}
		if string(b) != "hello" {
			t.Errorf("body = %q", b)
		}
	})
	t.Run("over limit", func(t *testing.T) {
		_, err := ReadLimited(strings.NewReader("hello world"), 5)
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("err = %v, want ResponseTooLargeError", err)
		}
	})
	t.Run("zero limit is unbounded", func(t *testing.T) {
		b, err := ReadLimited(strings.NewReader("hello world"), 0)
		if err != nil || string(b) != "hello world" {
			t.Fatalf("ReadLimited unbounded: body=%q err=%v", b, err)
		}
	})
}

// TestDo_ResponseTooLarge asserts that a body exceeding MaxResponseBytes is
// rejected with a typed error mapping to exit 22, rather than buffered whole.
func TestDo_ResponseTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("A", 4096)))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, nil)
	c.MaxResponseBytes = 1024
	_, err := c.Get(context.Background(), "/health", false)
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v, want ResponseTooLargeError", err)
	}
	if got := ExitCode(err); got != 22 {
		t.Errorf("ExitCode = %d, want 22", got)
	}
}
