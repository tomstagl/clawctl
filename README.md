# oc — openclaw client wrapper

A `kubectl`-style wrapper around the [openclaw](https://openclaw.ai) gateway. One binary, no dependencies beyond what's already on your machine, and a strong opinion that gateway interactions should be **safe by default**, **traceable**, and **secret-redacted at the boundary**.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash
```

The installer detects your OS + architecture, downloads the matching binary from the latest GitHub release, verifies the checksum, and installs to `/usr/local/bin/clawctl` (configurable via `CLAWCTL_INSTALL_DIR`).

## Quickstart

```bash
clawctl init                       # detect platform + print setup snippets; --check to verify config
clawctl health                     # gateway liveness
clawctl msg <agent> "hello"        # one-shot chat with any registered openclaw agent
```

## Setup

One required environment variable. Export it in `~/.zshrc` (or equivalent):

```bash
export CLAWCTL_HOST="http://your-openclaw-host:18789"
```

Store the bearer token in Keychain once:

```bash
security add-generic-password \
  -s openclaw-gateway-token \
  -a "$USER" \
  -w "<your-bearer-token>"
```

On Linux, set `CLAWCTL_TOKEN_CMD` to any command that prints the token (see [`docs/auth.md`](docs/auth.md) for `secret-tool` and `pass` recipes).

If you plan to use `clawctl cli`, also set `CLAWCTL_SSH_HOST`:

```bash
export CLAWCTL_SSH_HOST="ops@your-openclaw-host"   # only needed for `clawctl cli`
```

Optional knobs — sensible defaults exist:

| Variable                    | Default                  | Purpose                                                    |
| --------------------------- | ------------------------ | ---------------------------------------------------------- |
| `CLAWCTL_HOST`              | _required_               | Gateway URL                                                |
| `CLAWCTL_SSH_HOST`          | _unset_                  | SSH target for `clawctl cli`                               |
| `CLAWCTL_TOKEN_CMD`         | _unset_                  | Shell command that prints the bearer token                 |
| `CLAWCTL_KEYCHAIN_SERVICE`  | `openclaw-gateway-token` | macOS Keychain entry name                                  |
| `CLAWCTL_TIMEOUT`           | `60`                     | Per-call timeout (seconds)                                 |
| `CLAWCTL_CACHE_DIR`         | `~/.cache/clawctl`       | Models cache + redaction audit                             |
| `CLAWCTL_MODELS_TTL`        | `60`                     | `clawctl models` cache TTL (seconds)                       |
| `CLAWCTL_NO_REDACT`         | `0`                      | Set to `1` to bypass redaction (debug only)                |
| `CLAWCTL_JAEGER_UI`         | _unset_                  | Used by `clawctl trace` to print a working UI link         |

## Surface

| Command                                 | Purpose                                               |
| --------------------------------------- | ----------------------------------------------------- |
| `clawctl health`                        | Gateway liveness (no auth)                            |
| `clawctl models`                        | List registered agents (60s cached)                   |
| `clawctl msg [-s SESSION] AGENT [TEXT]` | One-shot chat; stdin if `TEXT` omitted                |
| `clawctl stream [-s SESSION] AGENT [TEXT]` | Same, SSE-buffered + redacted                      |
| `clawctl raw METHOD PATH [curl-args]`   | Arbitrary `/v1/...` call with auth + traceparent      |
| `clawctl cli SUBCOMMAND...`             | Run `openclaw …` over SSH on the gateway host         |
| `clawctl verify KIND ARGS`              | Claim verification — `commit`, `pr`, `issue`, `file`  |
| `clawctl trace TRACE-ID`               | Print Jaeger UI link + first 30 spans for a trace     |
| `clawctl init [--check]`               | Print platform setup snippets; verify config with `--check` |

Exit codes: see [`docs/cli-contract.md`](docs/cli-contract.md).

## Use from agents

- **Exit-code contract and `--json` output shapes** → [`docs/cli-contract.md`](docs/cli-contract.md)
- **MCP server setup and `clawctl_msg` tool reference** → [`docs/mcp.md`](docs/mcp.md)

```bash
# Register clawctl as an MCP server in Claude Code
claude mcp add clawctl --command clawctl --args mcp
```

## Install Plugin

Install the Claude Code plugin from this repo's marketplace in one step:

```bash
claude plugin marketplace add tomstagl/clawctl
claude plugin install clawctl
```

The plugin requires `clawctl` on PATH. If it's not installed yet, run the [Install](#install) command first.

## Use with Claude Code

This repo doubles as a Claude Code plugin and an MCP server. Once `clawctl` is on PATH, you have two integrations:

- **MCP server** — register `clawctl mcp` and Claude Code (or any MCP client) can call openclaw agents as typed tools. Traced and redacted at the boundary. See [`docs/mcp.md`](docs/mcp.md) for the full reference.
- **Slash-command plugin** — install with the commands in the [Install Plugin](#install-plugin) section above.

| Surface       | Name                | Purpose                                                                             |
| ------------- | ------------------- | ----------------------------------------------------------------------------------- |
| Slash command | `/clawctl`          | Drive openclaw — health, models, msg, cli, verify, trace.                           |
| Slash command | `/clawctl-recipes`  | Curated workflows (cron, sessions, debugging, redaction recovery, token rotation).  |
| Slash command | `/clawctl-cli`      | Full openclaw CLI reference (every subcommand + flags + examples).                  |
| Skill         | `openclaw-loopback` | Convention for openclaw agents delivering work via GitHub: labels, YAML header, R-1..R-12 rules. |

## Recipes

The most common workflows live in [`docs/recipes.md`](docs/recipes.md). Highlights:

- _"Is the gateway alive and what's it serving?"_
- _"Talk to a specific agent and capture the trace-id."_
- _"Schedule a daily brief at 07:00 local time."_
- _"Investigate a stuck or failed run."_
- _"Rotate the gateway token without dropping traffic."_

A full openclaw CLI reference lives at [`docs/cli-reference.md`](docs/cli-reference.md).

## Why this exists

The openclaw gateway speaks an OpenAI-compatible HTTP API plus a separate ops CLI on the host. Talking to it from a Mac means juggling auth tokens, traceparents for observability, redaction of leaked secrets, and SSH for ops commands. Doing that with raw `curl` works once; doing it 50 times a day is how secrets end up in shell history.

`clawctl` collapses that into one wrapper:

- **Auth**: bearer token pulled from macOS Keychain (or `CLAWCTL_TOKEN_CMD` on Linux) — never on disk, never in env.
- **Tracing**: a W3C `traceparent` is attached to every request. Trace-id is printed to stderr so you can cite it instead of dumping bodies.
- **Redaction**: outputs pass through a regex filter that masks `dt0c01.*`, `dt0s16.*`, `gh[psoru]_*`, AWS access keys, JWTs, and the gateway-token literal. Hits are audited at `~/.cache/clawctl/last-redaction` and warned to stderr.
- **Verification**: `clawctl verify {commit|pr|issue|file}` checks an agent's citation in one command. Exit 0 means verified.
- **Read-only by default**: anything mutating is gated behind the explicit `clawctl cli` subcommand, never an HTTP shortcut.

> **Naming note**: this `clawctl` shares a name with the OpenShift CLI. If you have both, alias one (`alias ocw=~/.local/bin/oc`) or rename the wrapper. The binary is self-contained — renaming is safe.

## Design principles

These are non-negotiable. PRs that violate them will be rejected.

1. **Read-only by default.** Mutating commands must be explicit, named, and never invoked autonomously.
2. **No secrets on disk.** Tokens live in Keychain. Env-only fallback is forbidden — `clawctl` would rather fail than read a token from a dotfile.
3. **Trace every call.** A traceparent is generated per invocation and printed to stderr. Reporting an issue means citing a trace-id, not a body.
4. **Redact at the boundary.** Even if upstream agents leak, the wrapper masks before output ever reaches the terminal. The audit log is append-only.
5. **One binary, zero runtime deps.** `ssh` (for `clawctl cli`) + `security` (macOS Keychain). That's it.

## Install from source

```bash
git clone https://github.com/tomstagl/clawctl.git
cd clawctl
go build -o /usr/local/bin/clawctl ./cmd/clawctl
```

### SSH connection reuse (recommended for `clawctl cli`)

`clawctl cli` shells out over SSH on every invocation. Enable ControlMaster reuse so subsequent calls within 10 minutes reuse the master connection:

```bash
./install/ssh-setup.sh
```

### oc-remote (required for `clawctl cli`)

`clawctl cli` requires `/usr/local/bin/oc-remote` on the gateway host. Install once:

```bash
ssh "$CLAWCTL_SSH_HOST" 'sudo install -m 0755 /dev/stdin /usr/local/bin/oc-remote' <<'OCREMOTE'
#!/usr/bin/env bash
set -euo pipefail
export PATH="$HOME/.npm-global/bin:$PATH"
exec openclaw "$@"
OCREMOTE
```

## Contributing

Bug reports and PRs welcome. Before opening a PR:

1. Read `docs/design-principles.md`.
2. Test against your own openclaw gateway (`clawctl health` should still pass).
3. Add or update a recipe in `docs/recipes.md` if you introduce a new pattern.

## License

MIT — see [LICENSE](LICENSE).
