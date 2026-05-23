---
name: clawctl
description: Reference for using the clawctl openclaw gateway CLI. Covers every subcommand, when to use HTTP vs SSH ops, output formats, env vars, and exit codes. Trigger whenever working with the openclaw gateway, running agents, or handling clawctl commands.
---

# clawctl — openclaw gateway CLI

## Environment (required before anything works)

```bash
CLAWCTL_HOST=http://openclaw:18789   # gateway URL — required for all HTTP commands
CLAWCTL_SSH_HOST=tom@openclaw        # SSH target — only needed for `clawctl cli`
```

Token is read from macOS Keychain (`openclaw-gateway-token`) or `CLAWCTL_TOKEN_CMD`. Never set it as a plain env var.

## Command reference

| Command | When to use |
|---------|-------------|
| `clawctl health` | Check gateway liveness before anything else. No auth needed. |
| `clawctl models` | List registered agents. **Use this, not `clawctl cli agents list`.** |
| `clawctl msg AGENT TEXT` | One-shot chat. Returns a JSON ToolResponse envelope. |
| `clawctl msg --text AGENT TEXT` | Same, but plain text — pipe-friendly. |
| `clawctl msg -s SESSION AGENT TEXT` | Continue a named session. |
| `clawctl stream AGENT TEXT` | SSE stream as NDJSON chunks + redacted. |
| `clawctl raw GET /v1/models` | Arbitrary authenticated API call. Useful for debugging. |
| `clawctl verify commit SHA` | Verify an agent's commit citation. Exit 0 = verified, 1 = not found. |
| `clawctl verify pr owner/repo#N` | Verify a PR citation. |
| `clawctl verify issue owner/repo#N` | Verify an issue citation. |
| `clawctl verify file path[@ref]` | Verify a file path citation. |
| `clawctl trace TRACE-ID` | Print Jaeger UI link + first 30 spans. |
| `clawctl init --check` | Verify `CLAWCTL_HOST`, token resolver, and gateway reachability. |
| `clawctl cli SUBCOMMAND...` | Admin ops over SSH — **only for commands with no HTTP equivalent**. |
| `clawctl mcp` | Start MCP stdio server (registered via `claude mcp add`). |

## Critical choices

**HTTP commands beat SSH for reads.** `clawctl models` is the right way to list agents — it uses `GET /v1/models` with auth, caching, and redaction. `clawctl cli agents list` adds an SSH round-trip for no benefit.

**Use `--text` when piping.** The default envelope is a JSON object (`{"agent":"...","content":"...","finish_reason":"...","redactions":[]}`). `--text` strips it to the raw content string.

**Use `--json` for structured output** from `health`, `models`, `verify`, and `trace` when the caller is a script. Each emits a single JSON envelope with `{"command":"...","ok":bool,"data":{...},"error":null}`.

**Session persistence.** `-s SESSION_ID` on `msg`/`stream` routes calls through the same gateway session. Use a stable string (e.g. the PR number, a task slug) as the session ID.

## Output shapes

**`clawctl msg`** (default envelope):
```json
{"agent":"openclaw/concierge","content":"...","finish_reason":"stop","usage":{"input_tokens":10,"output_tokens":8,"total_tokens":18},"redactions":[]}
```

**`clawctl stream`** — one NDJSON line per chunk, final line is a ToolResponse envelope.

**`clawctl verify`** — exits 0 on success, 1 if unverified, 2 on usage error.

**Stderr discipline.** Trace-id, redaction warnings, and error summaries go to stderr. Response bodies go to stdout. Safe to pipe `clawctl msg … | jq`.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Unverified (`verify` only) |
| 2 | Usage error / missing env var |
| 6 | DNS failure |
| 7 | Connection refused |
| 22 | HTTP 4xx/5xx |
| 28 | Timeout (`CLAWCTL_TIMEOUT`, default 60s) |

`clawctl cli` passes through the SSH and remote openclaw exit code unchanged.

## Redaction

Every response passes through a regex filter that masks Dynatrace tokens, GitHub tokens, AWS AKIDs, JWTs, and the gateway-token literal. Hits are warned to stderr and audited at `~/.cache/clawctl/last-redaction`. Never rely on redaction — agents must not emit secrets in the first place (see `openclaw-loopback` skill, R-11).

## MCP tool

After `claude mcp add clawctl -- clawctl mcp`, Claude Code can call `clawctl_msg` as a tool:
```json
{"tool": "clawctl_msg", "agent": "concierge", "message": "hello", "session_id": "optional"}
```
One MCP tool per registered agent. Traced and redacted identically to `clawctl msg`.

## Recipes

```bash
# Is the gateway alive?
clawctl health

# What agents are available?
clawctl models

# Ask an agent, get plain text
clawctl msg --text concierge "summarise open PRs"

# Stream a long-running response
clawctl stream concierge "run the build and report"

# Verify a citation from an agent's PR
clawctl verify commit abc123def456

# Investigate a failed run
clawctl trace <trace-id-from-stderr>

# Admin op that has no HTTP equivalent
clawctl cli openclaw logs --tail 50
```
