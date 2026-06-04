// Package api implements the authenticated HTTP transport that talks to the
// openclaw gateway's OpenAI-compatible REST surface. It mirrors the bash
// script's curl invocation: bearer-token auth from the Keychain (passed in
// by the caller as a TokenSource so this package stays platform-neutral),
// up to two retries with a 1s delay (curl --retry 2 --retry-connrefused
// --retry-delay 1 --retry-all-errors), and curl-aligned exit-code
// classification via typed errors.
//
// Errors are deliberately typed so the cmd/clawctl entry point can map each
// class onto the documented exit-code contract (6 DNS, 7 refused, 22 HTTP
// error, 28 timeout) without re-classifying low-level net package errors at
// every call site.
package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// TokenSource lazily produces a bearer token. It is called only for
// authenticated requests so that subcommands targeting public endpoints
// (e.g. /health) need not hit the Keychain.
type TokenSource func() (string, error)

// DefaultMaxResponseBytes caps how much of any single HTTP response body the
// client buffers into memory. The gateway's /v1/* surface returns small JSON
// (model lists, chat completions); a multi-gigabyte body is a misconfigured or
// hostile peer, not a legitimate response. Callers that genuinely need more can
// raise Client.MaxResponseBytes (CLAWCTL_MAX_RESPONSE_BYTES at the CLI layer).
const DefaultMaxResponseBytes int64 = 10 << 20 // 10 MiB

// Client is the openclaw gateway REST client.
type Client struct {
	Host    string
	Token   TokenSource
	Timeout time.Duration

	HTTP *http.Client

	// Retries is the number of retries after the initial attempt; curl's
	// --retry 2 means up to 3 total attempts.
	Retries    int
	RetryDelay time.Duration

	// MaxResponseBytes bounds the in-memory size of a single response body.
	// Zero or negative means unbounded (io.ReadAll); New seeds the default.
	MaxResponseBytes int64
}

// New constructs a Client with the documented retry/timeout defaults.
//
// Note: HTTP.Timeout is a single deadline covering DNS, dial, TLS, and the
// full body read — a large-but-valid response arriving slowly can therefore
// trip the timeout (exit 28) the same way curl --max-time does. Read-only
// metadata paths (e.g. models) fail open via the cache, so this is acceptable.
func New(host string, timeout time.Duration, token TokenSource) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		Host:             strings.TrimRight(host, "/"),
		Token:            token,
		Timeout:          timeout,
		HTTP:             &http.Client{Timeout: timeout},
		Retries:          2,
		RetryDelay:       time.Second,
		MaxResponseBytes: DefaultMaxResponseBytes,
	}
}

// ResponseTooLargeError means a response body exceeded the configured cap.
// It maps to exit 22 (an HTTP-level failure) so the caller treats an
// oversized body the same as any other bad gateway response.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("response body exceeded %d-byte limit", e.Limit)
}

// ReadLimited reads from r up to limit bytes, returning a ResponseTooLargeError
// if the source has more. A non-positive limit reads without bound. It is
// exported so non-Client HTTP paths (e.g. the best-effort Jaeger fetches) can
// apply the same ceiling.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	// Read one byte past the limit so we can distinguish "exactly at limit"
	// from "overflowed".
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, &ResponseTooLargeError{Limit: limit}
	}
	return b, nil
}

// DNSError means name resolution failed; maps to exit 6.
type DNSError struct {
	Host string
	Err  error
}

func (e *DNSError) Error() string { return fmt.Sprintf("DNS resolution failed for %s", e.Host) }
func (e *DNSError) Unwrap() error { return e.Err }

// ConnRefusedError means the TCP connection was refused; maps to exit 7.
type ConnRefusedError struct {
	Host string
	Err  error
}

func (e *ConnRefusedError) Error() string { return fmt.Sprintf("connection refused: %s", e.Host) }
func (e *ConnRefusedError) Unwrap() error { return e.Err }

// TimeoutError means the request exceeded the configured timeout; maps to
// exit 28.
type TimeoutError struct {
	Host    string
	Timeout time.Duration
	Err     error
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout (%s) calling %s", e.Timeout, e.Host)
}
func (e *TimeoutError) Unwrap() error { return e.Err }

// HTTPError carries the status code and body of a non-2xx response; maps to
// exit 22. Body is preserved so callers can pretty-print it on stdout the
// way curl --fail-with-body does for the bash entrypoint.
type HTTPError struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, strings.TrimSpace(string(e.Body)))
}

// ExitCode maps a transport error onto the documented curl-aligned exit
// code. Returns 0 for nil. Falls through to 1 for unrecognized errors so
// callers can still surface a non-zero exit instead of masking failures.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var dnsErr *DNSError
	if errors.As(err, &dnsErr) {
		return 6
	}
	var refErr *ConnRefusedError
	if errors.As(err, &refErr) {
		return 7
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return 22
	}
	var tooLarge *ResponseTooLargeError
	if errors.As(err, &tooLarge) {
		return 22
	}
	var toErr *TimeoutError
	if errors.As(err, &toErr) {
		return 28
	}
	return 1
}

// Request describes an outbound HTTP call. Used by Do for arbitrary methods
// and bodies (e.g. `clawctl raw POST …`); Get is the GET-only convenience
// wrapper that subcommands such as health and models still call.
type Request struct {
	// Method is the HTTP verb (GET, POST, …). Required.
	Method string
	// Path is appended to the client's Host; e.g. "/v1/models".
	Path string
	// Body is the request body. May be nil.
	Body []byte
	// Authed toggles the Authorization: Bearer <token> header. When true the
	// Client must have a non-nil TokenSource.
	Authed bool
	// Traceparent, if non-empty, is sent as the W3C `traceparent` header so
	// the gateway can correlate spans. The bash `clawctl raw` always sets
	// this; the typed binary should too.
	Traceparent string
	// Headers carries raw "Key: Value" strings the caller wants to add on
	// top of the defaults (Accept, Authorization, traceparent). Matches the
	// bash entrypoint's `-H` passthrough.
	Headers []string
	// Retry, when true, applies the standard retry policy (transport errors
	// and 5xx responses, up to c.Retries times). When false the call is
	// one-shot — bash `clawctl raw` only retries GETs, so callers gate this
	// on the verb.
	Retry bool
}

// Get issues an authenticated GET on path (e.g. "/v1/models") and returns
// the response body. When authed is false the Authorization header is
// omitted — used for /health, which the gateway exposes anonymously to
// match curl's bash invocation.
//
// Retries are applied for transport-level errors and for HTTP responses
// >= 500, mirroring curl's --retry-all-errors behavior. 4xx responses are
// returned without retry because they're caller-fault and won't recover.
func (c *Client) Get(ctx context.Context, path string, authed bool) ([]byte, error) {
	return c.Do(ctx, Request{
		Method: http.MethodGet,
		Path:   path,
		Authed: authed,
		Retry:  true,
	})
}

// Do executes req and returns the response body. Errors are typed to match
// the documented exit-code contract (DNSError → 6, ConnRefusedError → 7,
// HTTPError → 22, TimeoutError → 28).
func (c *Client) Do(ctx context.Context, req Request) ([]byte, error) {
	if c.Host == "" {
		return nil, errors.New("api: host is empty")
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	endpoint := c.Host + req.Path

	var token string
	if req.Authed {
		if c.Token == nil {
			return nil, errors.New("api: authenticated request requires a TokenSource")
		}
		t, err := c.Token()
		if err != nil {
			return nil, fmt.Errorf("api: token: %w", err)
		}
		token = t
	}

	attempts := 1
	if req.Retry {
		attempts = c.Retries + 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		body, err := c.doRequest(ctx, endpoint, token, req)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !req.Retry || !shouldRetry(err) || attempt == attempts-1 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, classifyContextErr(ctx.Err(), endpoint, c.Timeout)
		case <-time.After(c.RetryDelay):
		}
	}
	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, endpoint, token string, req Request) ([]byte, error) {
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if req.Traceparent != "" {
		httpReq.Header.Set("traceparent", req.Traceparent)
	}
	for _, h := range req.Headers {
		k, v, ok := splitHeader(h)
		if !ok {
			return nil, fmt.Errorf("api: malformed header %q (want 'Key: Value')", h)
		}
		httpReq.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, classifyTransportErr(err, endpoint, c.Timeout)
	}
	defer resp.Body.Close()

	body, readErr := ReadLimited(resp.Body, c.MaxResponseBytes)
	if readErr != nil {
		var tooLarge *ResponseTooLargeError
		if errors.As(readErr, &tooLarge) {
			return nil, readErr
		}
		return nil, classifyTransportErr(readErr, endpoint, c.Timeout)
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: body}
	}
	return body, nil
}

// splitHeader parses a `Key: Value` curl-style header string. Trailing/leading
// whitespace around the value is trimmed; the key is returned verbatim.
func splitHeader(s string) (string, string, bool) {
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:idx])
	val := strings.TrimSpace(s[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

func shouldRetry(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		// Caller-fault errors don't get retried — matches the principle of
		// not papering over a 4xx with a delay loop.
		return httpErr.StatusCode >= 500
	}
	var refErr *ConnRefusedError
	if errors.As(err, &refErr) {
		return true
	}
	var dnsErr *DNSError
	if errors.As(err, &dnsErr) {
		// curl --retry-all-errors retries DNS too, but that's almost never
		// useful — a freshly-failed DNS lookup will keep failing. We retry
		// once anyway for parity, then surface.
		return true
	}
	var toErr *TimeoutError
	if errors.As(err, &toErr) {
		// Timeout already exhausted the budget; retrying just wastes more.
		return false
	}
	// Generic transport error (e.g. peer reset): retry.
	return true
}

// classifyTransportErr inspects a low-level net/http error and turns it
// into one of the typed errors above. Anything we can't classify is
// returned as-is so callers still get the underlying error chain.
func classifyTransportErr(err error, endpoint string, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	host := hostOf(endpoint)

	if isTimeout(err) {
		return &TimeoutError{Host: host, Timeout: timeout, Err: err}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &DNSError{Host: host, Err: err}
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return &ConnRefusedError{Host: host, Err: err}
	}

	return err
}

func classifyContextErr(err error, endpoint string, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &TimeoutError{Host: hostOf(endpoint), Timeout: timeout, Err: err}
	}
	return err
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return true
	}
	return false
}

func hostOf(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		if u.Scheme != "" {
			return u.Scheme + "://" + u.Host
		}
		return u.Host
	}
	return endpoint
}
