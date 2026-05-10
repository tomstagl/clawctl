package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/config"
)

// installInMemoryMCPRun swaps the production stdio Run for an in-memory
// transport pair so unit tests can exercise the wiring without touching
// real stdin/stdout. Returns a (clientTransport, ready) pair: ready
// closes once runMCP enters mcpRun, after which the test may safely
// Connect on the client side. The done channel closes when srv.Run
// returns, so the test can wait for clean shutdown after closing the
// session.
func installInMemoryMCPRun(t *testing.T) (clientTransport mcp.Transport, ready <-chan struct{}, done <-chan struct{}) {
	t.Helper()
	prev := mcpRun
	ct, st := mcp.NewInMemoryTransports()
	r := make(chan struct{})
	d := make(chan struct{})
	mcpRun = func(ctx context.Context, srv *mcp.Server, _ io.Reader, _ io.Writer) error {
		close(r)
		err := srv.Run(ctx, st)
		close(d)
		return err
	}
	t.Cleanup(func() { mcpRun = prev })
	return ct, r, d
}

func TestRunMCP_MissingHost(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCP(context.Background(), config.Config{CacheDir: t.TempDir()}, nil, nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CLAWCTL_HOST not set") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunMCP_RejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cfg := config.Config{Host: "http://x", CacheDir: t.TempDir()}
	code := runMCP(context.Background(), cfg, []string{"oops"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unexpected argument "oops"`) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunMCP_NoAgentsExits1(t *testing.T) {
	withStubTokenSource(t, "tok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"non-openclaw/skipme"}]}`))
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
	code := runMCP(context.Background(), cfg, nil, nil, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no openclaw agents") {
		t.Errorf("stderr = %q, want 'no openclaw agents' marker", stderr.String())
	}
}

func TestRunMCP_FetchFailureMaps(t *testing.T) {
	withStubTokenSource(t, "tok")
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
	code := runMCP(context.Background(), cfg, nil, nil, &stdout, &stderr)
	if code != 7 {
		t.Errorf("exit = %d, want 7 (connection refused)", code)
	}
}

// TestRunMCP_ToolsListReturnsAtLeastOne is the in-process flavour of the
// US-025 acceptance criterion: against a mock /v1/models the server's
// tools/list returns the expected agents. The subprocess flavour lives in
// TestMCPEndToEnd_SpawnAndListTools below.
func TestRunMCP_ToolsListReturnsAtLeastOne(t *testing.T) {
	withStubTokenSource(t, "tok")

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openclaw/concierge","description":"helps users"},
			{"id":"openclaw/dead-code-sweep"}
		]}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		Host:            srv.URL,
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}

	clientTransport, ready, done := installInMemoryMCPRun(t)

	// runMCP blocks on srv.Run until the transport closes; run it on a
	// goroutine and tear down via cs.Close after the assertions.
	resCh := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		resCh <- runMCP(context.Background(), cfg, nil, nil, &stdout, &stderr)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not enter mcpRun in 5s; stderr=%s", stderr.String())
	}

	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cli.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("Connect: %v\nstderr=%s", err, stderr.String())
	}

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) < 1 {
		t.Fatalf("len(tools) = %d, want >= 1", len(res.Tools))
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"concierge", "dead-code-sweep"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}

	// Server-side stderr surface should announce tool count.
	if !strings.Contains(stderr.String(), "registered 2 tool(s)") {
		t.Errorf("stderr = %q, want 'registered 2 tool(s)'", stderr.String())
	}

	_ = cs.Close()
	<-done
	select {
	case code := <-resCh:
		if code != 0 {
			t.Errorf("runMCP exit = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("runMCP did not return after client close")
	}
}

// TestMCPEndToEnd_SpawnAndListTools is the subprocess flavour US-025
// names explicitly: spawn `clawctl mcp` as a child process, send tools/list
// over the MCP CommandTransport, assert at least one tool is returned.
//
// The mock /v1/models is a real httptest server; the binary is built fresh
// each test run via `go build` into the test's TempDir so we don't depend
// on a checked-in artifact. The build is the slow step (~1s) but it keeps
// the test self-contained — running `go test ./...` works on a fresh clone
// without a separate setup target.
func TestMCPEndToEnd_SpawnAndListTools(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess build is slow; skipped under -short")
	}

	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer e2e-token" {
			t.Errorf("auth = %q, want 'Bearer e2e-token'", got)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openclaw/main","description":"e2e demo agent"},
			{"id":"openclaw/test-coverage-filler"}
		]}`))
	}))
	defer models.Close()

	bin := buildClawctlForTest(t)
	cacheDir := t.TempDir()

	// Wire a fake `security` shim on PATH so the keychain reader returns
	// our test token without touching the real macOS Keychain.
	pathDir := t.TempDir()
	writeShim(t, filepath.Join(pathDir, "security"), `#!/bin/sh
# fake security: only respond to find-generic-password -w
case "$1" in
  find-generic-password)
    echo "e2e-token"
    ;;
  *)
    echo "unexpected security args: $*" >&2
    exit 1
    ;;
esac
`)

	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(),
		"CLAWCTL_HOST="+models.URL,
		"CLAWCTL_CACHE_DIR="+cacheDir,
		"CLAWCTL_KEYCHAIN_SERVICE=clawctl-e2e",
		"CLAWCTL_TIMEOUT=5",
		"CLAWCTL_MODELS_TTL=60",
		"PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	// Capture stderr separately so the SDK's stdout framing isn't
	// disturbed by our diagnostic prints.
	var serr bytes.Buffer
	cmd.Stderr = &serr

	transport := &mcp.CommandTransport{Command: cmd}
	cli := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := cli.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v\nstderr=%s", err, serr.String())
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v\nstderr=%s", err, serr.String())
	}
	if len(res.Tools) < 1 {
		t.Fatalf("len(tools) = %d, want >= 1\nstderr=%s", len(res.Tools), serr.String())
	}
	names := map[string]string{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool.Description
	}
	for _, want := range []string{"main", "test-coverage-filler"} {
		if _, ok := names[want]; !ok {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
	if got := names["main"]; got != "e2e demo agent" {
		t.Errorf("main.Description = %q, want 'e2e demo agent' (gateway-supplied)", got)
	}
}

// buildClawctlForTest compiles the typed binary into the test's TempDir
// and returns its path. CGO_ENABLED=0 is set so the build matches the
// release-workflow contract documented in US-029 (single static binary).
func buildClawctlForTest(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clawctl")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// writeShim writes a 0755 shell script to path. Mirrors the cli_test.go
// PATH-shim helper used by US-020/US-021.
func writeShim(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
}
