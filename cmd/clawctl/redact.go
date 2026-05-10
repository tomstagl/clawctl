package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tomstagl/clawctl/internal/config"
	"github.com/tomstagl/clawctl/internal/keychain"
	"github.com/tomstagl/clawctl/internal/redact"
)

// runRedact mirrors the bash entrypoint's hidden `_redact` subcommand:
// reads stdin, writes redacted text to stdout, prints the WARNING line
// and appends to the audit file when matches are found, and writes the
// per-hit JSON array to $CLAWCTL_REDACT_SINK when set. The agent label
// (positional arg, "?" if absent) flows into the WARNING and audit.
//
// This entry point exists for parity testing only and is intentionally
// absent from `clawctl help`. Production subcommands (msg, stream)
// will call internal/redact directly once US-018+ land.
func runRedact(cfg config.Config, agent string, stdin io.Reader, stdout, stderr io.Writer) int {
	in, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "clawctl: read stdin: %v\n", err)
		return 1
	}

	sinkPath := os.Getenv("CLAWCTL_REDACT_SINK")

	if cfg.NoRedact {
		// Honor the sink even on bypass so envelope emitters always see
		// a valid JSON array rather than a missing file (matches bash).
		if sinkPath != "" {
			_ = os.WriteFile(sinkPath, []byte("[]"), 0o644)
		}
		_, _ = stdout.Write(in)
		return 0
	}

	gw := readGwToken(cfg)
	r := redact.Apply(string(in), redact.Options{GwToken: gw})
	_, _ = io.WriteString(stdout, r.Text)

	if sinkPath != "" {
		_ = os.WriteFile(sinkPath, redact.MarshalSink(r.Hits), 0o644)
	}

	kinds := r.Kinds()
	if len(kinds) > 0 {
		fmt.Fprintln(stderr, redact.WarnLine(agent, kinds))
		if cfg.CacheDir != "" {
			_ = os.MkdirAll(cfg.CacheDir, 0o755)
			_ = redact.AppendAudit(filepath.Join(cfg.CacheDir, "last-redaction"), agent, kinds)
		}
	}
	return 0
}

// readGwToken returns the gateway bearer for literal-substring redaction.
// Failures are swallowed (return ""): the bash helper does the same with
// `_token 2>/dev/null || true` so a missing keychain item still lets
// pattern-based redactions run.
func readGwToken(cfg config.Config) string {
	tok, err := keychain.Token(cfg.KeychainService, "")
	if err != nil {
		return ""
	}
	return tok
}

// redactCmd is the entry-point wrapper used by main(). agent defaults to
// "?" when unspecified, matching the bash dispatcher's `${1:-?}`.
func redactCmd(cfg config.Config, args []string) {
	agent := "?"
	if len(args) > 0 && args[0] != "" {
		agent = args[0]
	}
	os.Exit(runRedact(cfg, agent, os.Stdin, os.Stdout, os.Stderr))
}
