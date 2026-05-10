# Register clawctl as an MCP server in Claude Code

`clawctl mcp` starts a stdio MCP server that exposes one tool per agent the gateway publishes under `/v1/models`. Claude Code (and any other MCP client — Codex, Continue, or a local LLM that speaks the protocol) can register the server once and then call openclaw agents as typed tools, with auth, tracing, and redaction handled by the same boundary the rest of the wrapper uses.

This doc is the one-step path: how to register, what envs are needed, what `tools/list` and `tools/call` actually look like.

## Prerequisites

- The typed `clawctl` binary on `PATH` (`brew install`, `curl | sh`, or `go build ./cmd/clawctl` from this repo).
- `CLAWCTL_HOST` set to the gateway URL.
- A bearer token stored in macOS Keychain under `openclaw-gateway-token` (or whatever `CLAWCTL_KEYCHAIN_SERVICE` points at).
- The gateway returning at least one `openclaw/<slug>` entry in `/v1/models` — `clawctl mcp` exits 1 with `ErrNoAgents` if the response is empty after stripping the prefix, because an MCP server with zero tools is almost always a misconfiguration (wrong host, missing auth) rather than a legitimate state.

If `clawctl health` and `clawctl models` work, `clawctl mcp` will work — it reuses the same transport, Keychain lookup, and 60s file cache (`$CLAWCTL_CACHE_DIR/models.json`) under the hood.

## Register with Claude Code

Run this once. Claude Code will spawn `clawctl mcp` as a subprocess on every session that needs the tools:

```bash
claude mcp add clawctl --command clawctl --args mcp
```

The command tells Claude Code to invoke `clawctl mcp` over stdio. There is no port to expose, no daemon to keep alive — when the Claude Code session ends, the subprocess exits.

Verify the registration in Claude Code:

```text
/mcp
```

You should see `clawctl` listed with a tool count matching the number of `openclaw/*` entries in `/v1/models`.

To remove later:

```bash
claude mcp remove clawctl
```

## Required environment

The MCP server inherits the parent process's environment, so the same envs that drive the rest of `clawctl` apply:

| Variable | Required? | Purpose |
| --- | --- | --- |
| `CLAWCTL_HOST` | yes | Gateway URL. `clawctl mcp` exits 2 if unset. |
| `CLAWCTL_KEYCHAIN_SERVICE` | no | Keychain service name for the bearer token. Default: `openclaw-gateway-token`. |
| `CLAWCTL_LOG` | no | Set to `json` to emit structured stderr logs (one JSON line per call). Default: human-friendly. The MCP framing owns stdout, so logs always go to stderr regardless of this knob. |
| `CLAWCTL_TIMEOUT` | no | Per-call timeout in seconds. Default: 60. |
| `CLAWCTL_NO_REDACT` | no | Set to `1` to bypass response redaction. **Debugging only** — leaks bypass the design-principle 4 contract. |
| `CLAWCTL_MODELS_TTL` | no | Seconds to cache `/v1/models`. Default: 60. |
| `CLAWCTL_CACHE_DIR` | no | Where the models cache + redaction audit live. Default: `~/.cache/clawctl`. |

The bearer token is **not** an env var. Store it in Keychain once:

```bash
security add-generic-password \
  -s openclaw-gateway-token \
  -a "$USER" \
  -w "<your-bearer-token>"
```

Claude Code's MCP subprocess inherits `$USER` and the Keychain ACL from your shell session, so the lookup succeeds without further setup.

## Worked example

Assume the gateway publishes two agents:

```json
{
  "data": [
    { "id": "openclaw/concierge", "description": "Routes requests to the right specialist agent.", "owned_by": "openclaw" },
    { "id": "openclaw/ops",       "description": "Operates the fleet: cron, sessions, skills.",  "owned_by": "openclaw" }
  ]
}
```

### `tools/list` output

When Claude Code calls `tools/list` on the registered `clawctl` server, it gets one MCP tool per agent. Tool names are the bare slug (the `openclaw/` prefix is stripped because some MCP clients reject `/` in tool identifiers); the description still cites the full slug so callers can route by full ID:

```json
{
  "tools": [
    {
      "name": "concierge",
      "description": "Routes requests to the right specialist agent.",
      "inputSchema": {
        "type": "object",
        "additionalProperties": false,
        "required": ["text"],
        "properties": {
          "text":        { "type": "string", "minLength": 1 },
          "session_id":  { "type": "string", "minLength": 1, "maxLength": 128 },
          "tool_choice": { "type": "string", "enum": ["auto", "none", "required"] },
          "streaming":   { "type": "boolean", "default": false }
        }
      }
    },
    {
      "name": "ops",
      "description": "Operates the fleet: cron, sessions, skills.",
      "inputSchema": { "...": "same shape as above" }
    }
  ]
}
```

The input schema mirrors `schemas/envelope.v1.json`'s `Input` shape one-to-one (`text`, optional `session_id`, optional `tool_choice`, optional `streaming`), so an LLM that already speaks the v1 envelope can call these tools without translating shapes.

### `tools/call` (non-streaming)

A typical call from Claude Code looks like:

```json
{
  "method": "tools/call",
  "params": {
    "name": "concierge",
    "arguments": {
      "text": "summarise overnight openclaw runs",
      "session_id": "ops-2026-05-10"
    }
  }
}
```

The tool result is a v1 `ToolResponse` envelope, returned as both `content[0].text` (a JSON string for clients that parse text) and `structuredContent` (typed value for clients using strict typing). The traceparent is echoed in `_meta.clawctl.traceparent` so clients can correlate Jaeger spans without parsing the envelope:

```json
{
  "_meta": { "clawctl.traceparent": "00-a1f8e5fa3b2c1d0e9f8a7b6c5d4e3f2a-1234567890abcdef-01" },
  "content": [{
    "type": "text",
    "text": "{\"envelope_version\":\"1\",\"kind\":\"tool_response\",\"agent\":\"openclaw/concierge\",\"traceparent\":\"00-a1f8e5fa3b2c1d0e9f8a7b6c5d4e3f2a-1234567890abcdef-01\",\"input\":{\"role\":\"user\",\"content\":\"summarise overnight openclaw runs\"},\"output\":\"3 runs completed; 1 surfaced a redacted secret (kind=dt0c01).\",\"redactions\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":18,\"total_tokens\":60},\"finish_reason\":\"stop\"}"
  }],
  "structuredContent": {
    "envelope_version": "1",
    "kind": "tool_response",
    "agent": "openclaw/concierge",
    "traceparent": "00-a1f8e5fa3b2c1d0e9f8a7b6c5d4e3f2a-1234567890abcdef-01",
    "input": { "role": "user", "content": "summarise overnight openclaw runs" },
    "output": "3 runs completed; 1 surfaced a redacted secret (kind=dt0c01).",
    "redactions": [],
    "usage": { "prompt_tokens": 42, "completion_tokens": 18, "total_tokens": 60 },
    "finish_reason": "stop"
  }
}
```

Cite the `traceparent` when reporting issues — that's the load-bearing identifier for the call across the gateway and Jaeger.

### `tools/call` (streaming)

Set `streaming: true` and supply a `progressToken` on the request to receive each `ToolStreamChunk` as an MCP `notifications/progress` message; the final `ToolResponse` is still returned as the tool result.

```json
{
  "method": "tools/call",
  "params": {
    "name": "ops",
    "arguments": { "text": "tail the fleet status", "streaming": true },
    "_meta": { "progressToken": "stream-1" }
  }
}
```

Each progress notification carries the chunk envelope under `_meta.clawctl.stream_chunk` (raw JSON so the client can validate against `schemas/envelope.v1.json` without an extra round-trip) and a 0-based `_meta.clawctl.stream_sequence` so consumers that only inspect `_meta` still see the ordering.

If a redaction pattern straddles two SSE chunks, `clawctl` coalesces into a single boundary-safe progress payload — emitting per-chunk in that case would either leak the unredacted bytes or split a redaction marker. This is the same trade-off `clawctl stream` makes; see `docs/transport-decisions.md`.

If the client did not supply a `progressToken`, the streaming flag is honoured on the gateway side (the server still parses and validates chunks) but no progress notifications fire — per the MCP spec, servers MUST NOT send progress for requests that did not opt in.

### Errors

Failures land in-band as a v1 `ToolError` envelope with `IsError=true`, so the LLM sees a structured failure on the content channel rather than an opaque transport reject. The `code` field follows the schema enum, and `exit_code` carries the `clawctl` exit code that a re-shelled `clawctl msg` would have produced:

```json
{
  "isError": true,
  "_meta": { "clawctl.traceparent": "00-…-01" },
  "structuredContent": {
    "envelope_version": "1",
    "kind": "tool_error",
    "agent": "openclaw/concierge",
    "traceparent": "00-…-01",
    "code": "gateway.rate_limited",
    "message": "gateway error: HTTP 429",
    "http_status": 429,
    "exit_code": 22
  }
}
```

The mapping from transport failure to envelope code mirrors the curl-aligned exit-code contract in `clawctl help`: `transport.connection_refused` → 7, `transport.dns` → 6, `transport.timeout` → 28, `gateway.*` → 22.

## Troubleshooting

- `clawctl: CLAWCTL_HOST not set` — export `CLAWCTL_HOST` in the shell that launches Claude Code, then restart the Claude Code session so the subprocess inherits it.
- `clawctl mcp: no openclaw agents in /v1/models response` — run `clawctl models` directly. If that returns an empty `data` array, the gateway has no agents (or your token isn't authorised to list them).
- `clawctl: keychain item "openclaw-gateway-token" not found` — store the token with `security add-generic-password` (see [Required environment](#required-environment)).
- Tool calls hang — check `clawctl health` first. The MCP subprocess inherits `CLAWCTL_TIMEOUT` (default 60s); long-running tool calls should set a higher timeout in the parent shell.
- Want to see what's happening on the wire? Set `CLAWCTL_LOG=json` and tail the Claude Code MCP log; one JSON line per call lands on the subprocess's stderr with `traceparent`, `agent`, `latency_ms`, and `redactions_count`.

## Security

`clawctl mcp` exposes **read-only** tools only. The `cli` subcommand — which SSHes to the gateway host and runs mutating `openclaw` operations — is intentionally excluded from the MCP surface.

**Why:** mutating SSH-driven ops (cron management, session teardown, fleet restarts) require a human in the loop to confirm intent. Exposing them over MCP would allow any connected LLM to trigger irreversible host-side changes without an interactive confirmation step.

**Future opt-in path:** if a mutating MCP tool is ever added, it must be gated by an explicit `--unsafe-mutating` flag on `clawctl mcp`. The flag must be absent by default; the server must refuse to register any mutating tool unless the flag is present; and the `tools/list` response must annotate each mutating tool with a human-readable warning. This design forces the operator to make an affirmative choice at startup rather than discovering the exposure at call time.

Until `--unsafe-mutating` exists, no path in `clawctl mcp` should register or invoke `cli`-equivalent functionality.

## Why a typed binary owns this

The `clawctl mcp` server lives in the typed Go binary, not the bash MVP, because the v1 envelope contract is enforced at compile time there (struct tags + `go:embed schemas/envelope.v1.json` + `envelope.Validate`). A bash MCP server would have to re-derive every shape on every emit and would drift the moment the schema gained a field. See [`docs/typed-binary-language.md`](typed-binary-language.md) for the full Go-vs-Rust decision.
