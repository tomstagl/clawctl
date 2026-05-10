package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/logging"
	"github.com/tomstagl/clawctl/internal/mcpserver"
)

// runMCP implements `clawctl mcp`. It registers four command-based MCP tools
// (clawctl_health, clawctl_models, clawctl_verify, clawctl_trace) without any
// startup network call, then serves them over an mcp.StdioTransport so any
// stdio MCP client can register clawctl with:
//
//	claude mcp add clawctl --command clawctl --args mcp
//
// The old agent-based server (one tool per openclaw/* model from /v1/models)
// is replaced by BuildCommandServer, which exposes typed read-only commands
// directly. We deliberately don't surface a CLAWCTL_LOG=json log line here:
// the MCP stdio protocol owns stdout, so JSON logs would corrupt the framing.
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

	tokenSource := keychainTokenSource(cfg)
	srv, err := mcpserver.BuildCommandServer(&mcpserver.Implementation{
		Name:    "clawctl",
		Title:   "clawctl — openclaw MCP gateway",
		Version: version,
	}, tokenSource, cfg.Host, cfg.JaegerUI)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl mcp: build server: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "clawctl mcp: registered 4 command tools; waiting for MCP client on stdio\n")
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

// mcpCmd is the entry-point wrapper used by main(). Threads the real
// stdin/stdout/stderr triple and exits with the documented code.
func mcpCmd(cfg config.Config, args []string) {
	code := runMCP(context.Background(), cfg, args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}
