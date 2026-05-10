package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/cache"
	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/logging"
	"github.com/tomstagl/clawctl/internal/mcpserver"
	"github.com/tomstagl/clawctl/internal/transport/api"
)

// runMCP implements `clawctl mcp`. It fetches /v1/models (reusing the
// 60s file cache that backs `clawctl models`), registers one MCP tool per
// returned openclaw/* slug, and runs the server on an mcp.StdioTransport
// so Claude Code / Codex / any other stdio MCP client can register
// clawctl with `claude mcp add clawctl --command clawctl --args mcp`.
//
// US-025 wires only tools/list. The per-tool handler is the package-level
// stub from internal/mcpserver; US-026 replaces it with the chat-completions
// path. We deliberately don't surface a CLAWCTL_LOG=json log line here: the
// MCP stdio protocol owns stdout, so JSON logs would corrupt the framing.
// The logging.Logger is still constructed so the human-mode WARNING/info
// surface stays consistent with the rest of the typed binary.
func runMCP(ctx context.Context, cfg config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	log := logging.New(cfg.Log, stderr, "mcp", logging.TransportAPI)
	defer func() { code = log.Finish(code) }()
	stderr = log.Stderr()

	if len(args) > 0 {
		fmt.Fprintf(stderr, "clawctl mcp: unexpected argument %q (mcp takes no positional args)\n", args[0])
		return 2
	}
	if cfg.Host == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_HOST not set. Export it (e.g. export CLAWCTL_HOST=http://your-openclaw-host:18789).")
		return 2
	}
	if cfg.CacheDir == "" {
		fmt.Fprintln(stderr, "clawctl: CLAWCTL_CACHE_DIR is empty (HOME unresolved?)")
		return 2
	}

	cachePath := filepath.Join(cfg.CacheDir, "models.json")
	tokenSource := keychainTokenSource(cfg)
	client := api.New(cfg.Host, cfg.Timeout, tokenSource)

	body, err := cache.Get(cachePath, cfg.ModelsTTL, func() ([]byte, error) {
		return client.Get(ctx, "/v1/models", true)
	})
	if err != nil {
		return reportMCPFetchError(cfg, err, stderr)
	}

	agents, err := mcpserver.ParseModels(body)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl mcp: parse /v1/models: %v\n", err)
		return 1
	}
	if len(agents) == 0 {
		fmt.Fprintf(stderr, "clawctl mcp: %v (gateway %s returned 0 openclaw/* slugs)\n", mcpserver.ErrNoAgents, cfg.Host)
		return 1
	}

	srv, err := mcpserver.Build(&mcpserver.Implementation{
		Name:    "clawctl",
		Title:   "clawctl — openclaw MCP gateway",
		Version: version,
	}, agents, nil)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl mcp: build server: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "clawctl mcp: registered %d tool(s); waiting for MCP client on stdio\n", len(agents))
	if err := mcpRun(ctx, srv, stdin, stdout); err != nil {
		// io.EOF on stdin is the normal shutdown signal when the parent
		// MCP client closes; treat it as a clean exit.
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(stderr, "clawctl mcp: %v\n", err)
		return 1
	}
	return 0
}

// mcpRun is split out so tests can swap in an in-memory transport without
// reaching for os.Stdin / os.Stdout. Production wiring uses the SDK's
// StdioTransport, which speaks newline-delimited JSON-RPC over the
// process stdin/stdout pair.
var mcpRun = func(ctx context.Context, srv *mcp.Server, _ io.Reader, _ io.Writer) error {
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func reportMCPFetchError(cfg config.Config, err error, stderr io.Writer) int {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		fmt.Fprintf(stderr, "clawctl mcp: gateway error fetching /v1/models: HTTP %d\n", httpErr.StatusCode)
		return 22
	}
	var dnsErr *api.DNSError
	if errors.As(err, &dnsErr) {
		fmt.Fprintf(stderr, "clawctl mcp: DNS resolution failed for %s\n", cfg.Host)
		return 6
	}
	var refErr *api.ConnRefusedError
	if errors.As(err, &refErr) {
		fmt.Fprintf(stderr, "clawctl mcp: connection refused: %s\n", cfg.Host)
		return 7
	}
	var toErr *api.TimeoutError
	if errors.As(err, &toErr) {
		fmt.Fprintf(stderr, "clawctl mcp: timeout (%ds) calling %s\n", int(cfg.Timeout.Seconds()), cfg.Host)
		return 28
	}
	if errors.Is(err, keychain.ErrNotFound) {
		fmt.Fprintf(stderr, "clawctl: keychain item %q not found. Add a token with: security add-generic-password -s %s -a $USER -w\n",
			cfg.KeychainService, cfg.KeychainService)
		return 2
	}
	fmt.Fprintf(stderr, "clawctl mcp: %v\n", err)
	return api.ExitCode(err)
}

// mcpCmd is the entry-point wrapper used by main(). Threads the real
// stdin/stdout/stderr triple and exits with the documented code.
func mcpCmd(cfg config.Config, args []string) {
	code := runMCP(context.Background(), cfg, args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
