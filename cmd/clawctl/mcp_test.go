package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestRunMCP_ToolsListReturnsFourCommandTools verifies that the command-based
// MCP server registers exactly the four read-only command tools and that
// tools/list returns them without any startup network call.
func TestRunMCP_ToolsListReturnsFourCommandTools(t *testing.T) {
	withStubTokenSource(t, "tok")

	// Host must be non-empty to pass the cfg.Host check, but no actual
	// request is made at startup — the command server registers tools
	// statically.
	cfg := config.Config{
		Host:            "http://mock:9999",
		CacheDir:        t.TempDir(),
		KeychainService: "test",
		Timeout:         2 * time.Second,
		ModelsTTL:       60 * time.Second,
	}

	clientTransport, ready, done := installInMemoryMCPRun(t)

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
	if len(res.Tools) != 4 {
		t.Fatalf("len(tools) = %d, want 4\ntools=%v\nstderr=%s", len(res.Tools), res.Tools, stderr.String())
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"clawctl_health", "clawctl_models", "clawctl_verify", "clawctl_trace"} {
		if !names[want] {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}

	if !strings.Contains(stderr.String(), "registered 4 command tools") {
		t.Errorf("stderr = %q, want 'registered 4 command tools' marker", stderr.String())
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

// TestMCPEndToEnd_SpawnAndListTools is the subprocess flavour: spawn
// `clawctl mcp` as a child process, send tools/list over the MCP
// CommandTransport, and assert the four command tools are returned.
//
// The binary is built fresh each test run via `go build` into the test's
// TempDir so we don't depend on a checked-in artifact.
func TestMCPEndToEnd_SpawnAndListTools(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess build is slow; skipped under -short")
	}

	bin := buildClawctlForTest(t)
	cacheDir := t.TempDir()

	// Wire a fake `security` shim on PATH so the keychain reader does not
	// touch the real macOS Keychain. The token is never actually retrieved
	// for a tools/list call; the shim is here for defence.
	pathDir := t.TempDir()
	writeShim(t, filepath.Join(pathDir, "security"), `#!/bin/sh
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
		"CLAWCTL_HOST=http://localhost:19999", // unused at tools/list time
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
	if len(res.Tools) != 4 {
		t.Fatalf("len(tools) = %d, want 4\nstderr=%s", len(res.Tools), serr.String())
	}
	names := map[string]string{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool.Description
	}
	for _, want := range []string{"clawctl_health", "clawctl_models", "clawctl_verify", "clawctl_trace"} {
		if _, ok := names[want]; !ok {
			t.Errorf("tools/list missing %q; got %v", want, names)
		}
	}
}

// buildClawctlForTest compiles the typed binary into the test's TempDir
// and returns its path. CGO_ENABLED=0 is set so the build matches the
// release-workflow contract documented in US-002 (single static binary).
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

// writeShim writes a 0755 shell script to path.
func writeShim(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}
}
