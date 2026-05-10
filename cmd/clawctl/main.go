// clawctl — typed Go entry point. Currently scaffolding (US-011): help text,
// env loading, and keychain wiring only. Subcommand implementations land in
// later stories (US-013…US-024).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/tomstagl/clawctl/internal/config"
)

// Set at build time via -ldflags '-X main.version=...'. Stay nominal until
// the release workflow lands (US-029).
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cfg := config.Load()
	args := os.Args[1:]

	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "help", "--help", "-h", "":
		printHelp(os.Stdout, cfg)
		os.Exit(0)
	case "version", "--version":
		fmt.Printf("clawctl %s (%s)\n", version, commit)
		os.Exit(0)
	case "health":
		healthCmd(cfg)
	case "models":
		modelsCmd(cfg)
	case "msg":
		msgCmd(cfg, args[1:])
	case "stream":
		streamCmd(cfg, args[1:])
	case "raw":
		rawCmd(cfg, args[1:])
	case "cli":
		cliCmd(cfg, args[1:])
	case "verify":
		verifyCmd(cfg, args[1:])
	case "_redact":
		// Hidden parity-test surface (mirrors the bash dispatcher's
		// `_redact)` branch). Not advertised in help.
		redactCmd(cfg, args[1:])
	default:
		// Subcommands are not yet implemented in the typed binary; the bash
		// entrypoint at ./clawctl is still authoritative. We deliberately exit
		// 2 (unknown command) to match the bash dispatcher's contract.
		fmt.Fprintf(os.Stderr, "clawctl: subcommand %q is not yet implemented in the typed binary; use ./clawctl (bash) until US-013…US-024 land\n", cmd)
		os.Exit(2)
	}
}

func printHelp(w io.Writer, cfg config.Config) {
	host := cfg.Host
	if host == "" {
		host = "<unset>"
	}
	fmt.Fprintf(w, `clawctl — openclaw client (host: %s)

  clawctl health                              gateway liveness
  clawctl models                              list registered agents (60s cache)
  clawctl msg [-s SESSION] [--text] AGENT [TEXT]
                                              chat with agent; stdin if no text
                                              default: emits a v1 ToolResponse JSON document
                                              --text: emits the plain content string (bash parity)
  clawctl stream [-s SESSION] [--text] AGENT [TEXT]
                                              same, SSE; redacted then emitted
                                              default: emits NDJSON ToolStreamChunks + final ToolResponse
                                              --text: emits buffered plain content (bash parity)
  clawctl raw METHOD PATH [curl-args]         arbitrary call with auth + traceparent
  clawctl cli SUBCOMMAND...                   run `+"`openclaw …`"+` over SSH on host
  clawctl verify KIND ARGS                    R-2 claim verification (see 'clawctl verify help')
  clawctl trace TRACE-ID                      lookup hint for a trace id

Required env:
  CLAWCTL_HOST              gateway URL (e.g. http://your-openclaw-host:18789)
  CLAWCTL_SSH_HOST          user@host for the gateway machine (only required for 'clawctl cli')
  CLAWCTL_JAEGER_UI         Jaeger base URL (only required for 'clawctl trace')

Optional env:
  CLAWCTL_KEYCHAIN_SERVICE  keychain service for the bearer token (default: openclaw-gateway-token)
  CLAWCTL_TIMEOUT           per-call timeout in seconds (default 60)
  CLAWCTL_NO_REDACT=1       disable client-side response redaction (NOT recommended)
  CLAWCTL_MODELS_TTL        seconds to cache /v1/models (default 60)
  CLAWCTL_LOG=json          emit one JSON log line per call on stderr (default: human-friendly)

Exit codes (transport):
  0   ok
  2   usage error, missing env var, unknown subcommand
  6   DNS resolution failed
  7   connection refused
  22  HTTP 4xx/5xx (body printed; reason on stderr)
  28  timeout

Subcommand-specific exit codes (rationale):
  verify    1 = unverified (commit/PR/issue/file not found); see 'clawctl verify help'
  cli       pass-through: ssh and oc-remote/openclaw exit codes reach the caller unchanged
  trace     best-effort: returns 0 even when Jaeger is unreachable so the UI link still surfaces
`, host)
}
