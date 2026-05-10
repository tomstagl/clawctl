---
description: Drive openclaw via the clawctl wrapper. Read-only by default, JSON-first, traceparent on every call, secrets redacted at the boundary.
---

Drive openclaw via the existing `clawctl` wrapper (https://github.com/tomstagl/clawctl). Behaves kubectl-style: read-only by default, use `--json | jq` for filtering, surface trace-id on every call, never bypass the wrapper.

Task: $ARGUMENTS

## Binary check (run first)

Before doing anything else, run:

```bash
command -v clawctl
```

If `clawctl` is not found, immediately print this install one-liner and stop — do NOT attempt raw `curl` against the gateway:

```bash
curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash
```

Then ask the user to re-open their shell and re-run the slash command.

## Setup (one-time, user)

The wrapper reads three env vars. The user MUST export `CLAWCTL_HOST`; the rest have sensible defaults:

```bash
# REQUIRED — gateway URL (where openclaw is reachable)
export CLAWCTL_HOST="http://<your-openclaw-host>:18789"

# OPTIONAL
export CLAWCTL_KEYCHAIN_SERVICE="openclaw-gateway-token"   # macOS keychain entry holding the bearer token
export CLAWCTL_TIMEOUT="60"                                # per-call timeout (s)
export CLAWCTL_JAEGER_UI="http://<jaeger-host>:16686"      # for `clawctl trace` to print a working UI link
```

If `clawctl health` fails with `connection refused` or `DNS resolution failed`, the user has not pointed `CLAWCTL_HOST` at a reachable gateway. Stop and ask them to set it. Do NOT guess a hostname.

If `clawctl` itself is not on PATH, run the binary check at the top of this prompt and print the install one-liner.

## Wrapper surface (already installed)

Use these directly — they handle auth, traceparent, redaction, and error explanation:

| Command                               | Purpose                                             |
| ------------------------------------- | --------------------------------------------------- |
| `clawctl health`                           | Gateway liveness (no auth)                          |
| `clawctl models`                           | List registered agents (60s cached)                 |
| `clawctl msg [-s SESSION] AGENT [TEXT]`    | One-shot chat; stdin if `TEXT` omitted              |
| `clawctl stream [-s SESSION] AGENT [TEXT]` | Same, SSE; output buffered + redacted               |
| `clawctl raw METHOD PATH [curl-args]`      | Arbitrary `/v1/...` call with auth + traceparent    |
| `clawctl cli SUBCOMMAND...`                | Run `openclaw …` over SSH on the gateway host       |
| `clawctl verify KIND ARGS`                 | Claim verification (commit / pr / issue / file)     |
| `clawctl trace TRACE-ID`                   | Print Jaeger UI link + first 30 spans for a trace   |

Exit codes: `0` ok, `6` DNS, `7` refused, `22` HTTP 4xx/5xx, `28` timeout, `2` usage. Trace-id is always printed to stderr.

## Decision tree — pick the right call

1. **"Is openclaw up?"** → `clawctl health` (don't ssh; the gateway answers HTTP).
2. **"What agents exist?"** → `clawctl models` (cached) or `clawctl cli agents list --json`.
3. **"Talk to agent X."** → `clawctl msg X "<prompt>"` for fast one-shots; add `-s <session-key>` to keep continuity. Use `clawctl stream` only when the agent is known to be slow — output is buffered identically anyway.
4. **"Run an openclaw CLI op (cron / sessions / skills / hooks / channels / approvals / secrets / status / models / mcp / tasks / plugins / dns / gateway / doctor / logs / agents)."** → `clawctl cli <command> [...] --json` then pipe to `jq`.
5. **"Inspect a trace seen in stderr."** → `clawctl trace <32-hex-trace-id>`.
6. **"Verify an agent's claim."** → `clawctl verify {commit|pr|issue|file} ...` (exit 0 = verified).
7. **"Hit an OpenAI-compatible endpoint directly."** → `clawctl raw GET /v1/models` etc.

Never invoke `curl` against the gateway directly when an `clawctl` subcommand fits — you'd lose redaction, traceparent, and error explanation.

## Token-efficient defaults

- Always pass `--json` to `clawctl cli` listing commands and pipe through `jq` to surface only the fields you need. Examples:
  - `clawctl cli agents list --json | jq -r '.[] | "\(.id)\t\(.workspace)"'`
  - `clawctl cli cron list --json --limit 20 | jq -r '.[] | "\(.id)\t\(.cron // .at)\t\(.name)"'`
  - `clawctl cli sessions --all-agents --json --limit 50 | jq -r '.[] | "\(.lastActivity)\t\(.key)"'`
- Never dump full JSON into the conversation when a 3-column projection answers the question.

## Safety rules (kubectl-style)

- **Read-only by default.** Anything that mutates state (`clawctl cli cron add|edit|run`, `clawctl cli agents add|delete|bind|unbind`, `clawctl cli channels add|remove|login|logout`, `clawctl cli skills install|update`, `clawctl cli plugins install|uninstall|update`, `clawctl cli secrets configure|apply`, `clawctl cli approvals set|allowlist`, `clawctl cli sessions cleanup --enforce`, `clawctl cli gateway install|start|stop|restart|uninstall`, `clawctl cli doctor --repair|--force`) requires explicit user confirmation first. Echo the exact command and wait.
- **Never run `clawctl cli ... --apply` or `--enforce` autonomously.**
- **Treat redaction warnings as boundary leaks.** If the wrapper writes `WARNING: redacted secret pattern(s)…` to stderr, surface it to the user and recommend rotating the matching credential. Audit log: `~/.cache/clawctl/last-redaction`.
- **Quote claims, not values**: when reporting back, cite paths/refs/IDs. Don't echo secret material even when the redactor catches it.
- **Cite a trace-id, not a body**, when reporting a gateway error. Pattern: `trace-id: <32-hex>`.

## Going deeper

Two companion slash commands ship with this plugin:

- `/clawctl-recipes` — step-by-step recipes (schedule a cron, prune sessions, investigate a stuck run, install a skill, rotate a token).
- `/clawctl-cli` — the full openclaw CLI reference (every subcommand + flags + examples).

Read those only when the task at hand isn't covered above. Most queries finish with one `clawctl` call.

## GitHub loop-back convention

If the project uses agents that deliver work via GitHub issues / PRs, see the `openclaw-loopback` skill that ships with this plugin — it documents the label scheme (`openclaw` + `openclaw:<slug>`), the YAML deliverable header (agent / run-id / traceparent / status), and the R-rules each agent must honour.
