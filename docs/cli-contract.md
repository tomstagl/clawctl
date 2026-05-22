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

### `clawctl cli` environment variables

| Variable              | Default                           | Purpose |
|-----------------------|-----------------------------------|---------|
| `CLAWCTL_SSH_HOST`    | _required_                        | SSH target (`user@host`) for the gateway host |
| `CLAWCTL_REMOTE_PATH` | `/usr/local/bin/clawctl-remote`   | Install path for `clawctl-remote` on the gateway host. Set to a user-writable path (e.g. `~/.local/bin/clawctl-remote`) when the default requires root |

### Stderr vs stdout discipline

| Stream | Content |
|--------|---------|
| stdout | Response body (JSON or plain text), envelope documents, error body for HTTP 4xx/5xx (curl parity) |
| stderr | Trace-id (`traceparent=…`), redaction warnings, human-readable error summaries, JSON log lines (`CLAWCTL_LOG=json`) |

This discipline lets callers pipe `clawctl … | jq …` without polluting the
JSON stream with diagnostic text.

## Stable JSON output (`--json` / `CLAWCTL_OUTPUT=json`)

Pass `--json` before or after the subcommand (or set `CLAWCTL_OUTPUT=json`) to
make `health`, `models`, `msg`, `verify`, and `trace` emit a single JSON object
on stdout instead of their default prose/envelope output.

### Envelope shape

```json
{
  "command": "<subcommand>",
  "ok": true,
  "data": { ... },
  "error": null
}
```

On error:

```json
{
  "command": "<subcommand>",
  "ok": false,
  "data": null,
  "error": {
    "code": "<string>",
    "message": "<string>",
    "trace_id": "<string or omitted>"
  }
}
```

The `error.code` field maps to exit codes:

| `error.code`  | Exit code | Condition |
|---------------|-----------|-----------|
| `usage_error` | 2         | Missing env var, unknown flag, or invalid argument |
| `dns_failure` | 6         | DNS resolution failed |
| `conn_refused`| 7         | Connection refused |
| `http_error`  | 22        | Gateway returned 4xx/5xx |
| `timeout`     | 28        | Request exceeded `CLAWCTL_TIMEOUT` |
| `not_found`   | 1         | `verify`: commit/PR/issue/file not found |
| `error`       | other     | Unclassified error |

Trace-id is still written to stderr even when `--json` is set.
Redaction is applied to `data` fields before output.

### `data` shapes per command

**`health`**: the raw JSON body returned by `GET /health`.

```json
{"command":"health","ok":true,"data":{"status":"ok"},"error":null}
```

**`models`**: the raw JSON body returned by `GET /v1/models`.

```json
{"command":"models","ok":true,"data":{"object":"list","data":[...]},"error":null}
```

**`msg`**: the core ToolResponse fields (no envelope metadata).

```json
{
  "command": "msg",
  "ok": true,
  "data": {
    "agent": "openclaw/concierge",
    "content": "Hello! How can I help?",
    "finish_reason": "stop",
    "usage": {"input_tokens": 10, "output_tokens": 8, "total_tokens": 18},
    "redactions": []
  },
  "error": null
}
```

**`verify`**: the human-readable verification message.

```json
{"command":"verify","ok":true,"data":{"message":"verified: commit deadbeef"},"error":null}
```

**`trace`**: trace-id, Jaeger UI URL, and span count (when Jaeger is reachable).

```json
{
  "command": "trace",
  "ok": true,
  "data": {
    "trace_id": "abc123...",
    "ui_url": "http://jaeger:16686/trace/abc123...",
    "spans_count": 5
  },
  "error": null
}
```

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
