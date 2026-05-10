# CLI Contract

This document defines the stable, versioned contract that `clawctl` exposes to
callers — scripts, agents, CI pipelines. It covers exit codes and their
conditions. The contract is versioned with the binary; breaking changes appear
in `CHANGELOG.md`.

## Exit codes

| Code | Condition |
|------|-----------|
| 0    | Success — the call completed without error and the response was written to stdout |
| 2    | Usage error — missing required env var (e.g. `CLAWCTL_HOST`), unknown subcommand, or invalid flag/argument |
| 6    | DNS resolution failed — the gateway hostname could not be resolved |
| 7    | Connection refused — the gateway host was reachable but the port rejected the TCP connection |
| 22   | HTTP error — the gateway returned a 4xx or 5xx response; the body is written to stdout and a summary to stderr |
| 28   | Timeout — the request exceeded `CLAWCTL_TIMEOUT` (default 60 s) |

Codes 6, 7, 22, and 28 mirror `curl`'s documented exit codes so callers can
treat `clawctl` and `curl` interchangeably in shell error-handling logic.

### Subcommand-specific codes

| Subcommand | Code | Condition |
|------------|------|-----------|
| `verify`   | 1    | Unverified — the commit/PR/issue/file was not found or the reference did not match |
| `cli`      | *    | Pass-through — the SSH session and remote `openclaw` exit code reach the caller unchanged |
| `trace`    | 0    | Best-effort — returns 0 even when Jaeger is unreachable so the UI link still surfaces |

### Stderr vs stdout discipline

| Stream | Content |
|--------|---------|
| stdout | Response body (JSON or plain text), envelope documents, error body for HTTP 4xx/5xx (curl parity) |
| stderr | Trace-id (`traceparent=…`), redaction warnings, human-readable error summaries, JSON log lines (`CLAWCTL_LOG=json`) |

This discipline lets callers pipe `clawctl … | jq …` without polluting the
JSON stream with diagnostic text.

### Scripting example

```bash
if ! clawctl health >/dev/null 2>&1; then
  case $? in
    6)  echo "DNS failure — check CLAWCTL_HOST" ;;
    7)  echo "Gateway down — port refused" ;;
    22) echo "Gateway returned an HTTP error" ;;
    28) echo "Gateway timed out" ;;
    *)  echo "Unexpected error $?" ;;
  esac
  exit 1
fi
```
