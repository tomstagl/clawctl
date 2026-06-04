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

### Registration

Run this once. Claude Code will spawn `clawctl mcp` as a subprocess on every session that needs the tools:

```bash
claude mcp add clawctl --command clawctl --args mcp
```

Verify it registered:

```text
/mcp
```

You should see `clawctl` listed with five tools.

### `tools/list` output

`clawctl mcp` registers a fixed set of typed read-only command tools — no startup network call, no per-agent tool list:

```json
{
  "tools": [
    { "name": "clawctl_health",  "description": "Check the openclaw gateway health endpoint." },
    { "name": "clawctl_models",  "description": "List available openclaw agent models from the gateway." },
    { "name": "clawctl_verify",  "description": "Verify a git commit, GitHub PR/issue, or file path." },
    { "name": "clawctl_trace",   "description": "Look up a W3C trace-id in Jaeger." },
    { "name": "clawctl_msg",     "description": "Send a prompt to an openclaw agent and receive a ToolResponse envelope." }
  ]
}
```

### `clawctl_msg` tool call

Send a prompt to an openclaw agent from any MCP client:

```json
{
  "method": "tools/call",
  "params": {
    "name": "clawctl_msg",
    "arguments": {
      "agent": "concierge",
      "text": "summarise overnight openclaw runs",
      "session_id": "ops-2026-05-10"
    }
  }
}
```

The tool result is a v1 `ToolResponse` envelope serialised as `content[0].text`. Redaction is applied before the response is returned — any secret patterns matched in the agent's output are replaced with `<REDACTED:kind:prefix…>` and listed in `redactions[]`:

```json
{
  "content": [{
    "type": "text",
    "text": "{\"envelope_version\":\"1\",\"kind\":\"tool_response\",\"agent\":\"openclaw/concierge\",\"input\":{\"role\":\"user\",\"content\":\"summarise overnight openclaw runs\"},\"output\":\"3 runs completed; 1 surfaced a redacted secret (kind=dt0c01).\",\"redactions\":[{\"kind\":\"dt0c01\",\"offset_hint\":52,\"count\":1}],\"usage\":{\"input_tokens\":42,\"output_tokens\":18,\"total_tokens\":60},\"finish_reason\":\"stop\"}"
  }]
}
```

Optional inputs for `clawctl_msg`:

| Field | Type | Description |
| --- | --- | --- |
| `agent` | string (required) | openclaw agent slug, e.g. `concierge` |
| `text` | string (required) | Prompt text to send |
| `session_id` | string | Resumes a prior conversation by the same session key (A2A `contextId`) |
| `task_id` | string | Optional unit-of-work id echoed back on the response envelope (A2A `taskId`); see `docs/agent-protocol.md` |
| `tool_choice` | `auto` \| `none` \| `required` | Hints to the agent about sub-tool routing |

### Errors

On transport or gateway failure the tool result has `isError: true` and the text content names the error:

```json
{
  "isError": true,
  "content": [{ "type": "text", "text": "clawctl_msg: HTTP 401: unauthorized" }]
}
```

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
