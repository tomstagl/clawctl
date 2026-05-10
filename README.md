# oc — openclaw client wrapper

A `kubectl`-style wrapper around the [openclaw](https://openclaw.ai) gateway. One binary, no dependencies beyond what's already on your Mac, and a strong opinion that gateway interactions should be **safe by default**, **traceable**, and **secret-redacted at the boundary**.

```bash
clawctl health                                                 # gateway liveness
clawctl models                                                 # registered agents (60s cached)
clawctl msg ops "summarise overnight openclaw runs"            # one-shot chat
clawctl cli cron list --json | jq -r '.[] | "\(.id)\t\(.cron)"'
clawctl verify pr tomstagl/studio-master#42                    # validate an agent's claim
clawctl trace a1f8e5fa…                                        # Jaeger UI link + first 30 spans
```

> **Naming note**: this `clawctl` shares a name with the OpenShift CLI. If you have both, alias one (`alias ocw=~/.local/bin/oc`) or rename the wrapper. The script is self-contained — renaming the file is safe.

## Why this exists

The openclaw gateway speaks an OpenAI-compatible HTTP API plus a separate ops CLI on the host. Talking to it from a Mac means juggling auth tokens, traceparents for observability, redaction of leaked secrets, and SSH for ops commands. Doing that with raw `curl` works once; doing it 50 times a day is how secrets end up in shell history.

`clawctl` collapses that into one wrapper:

- **Auth**: bearer token pulled from macOS Keychain — never on disk, never in env.
- **Tracing**: a W3C `traceparent` is attached to every request. Trace-id is printed to stderr so you can cite it instead of dumping bodies.
- **Redaction**: outputs pass through a regex filter that masks `dt0c01.*`, `dt0s16.*`, `gh[psoru]_*`, AWS access keys, JWTs, and the gateway-token literal. Hits are audited at `~/.cache/clawctl/last-redaction` and warned to stderr.
- **Verification**: `clawctl verify {commit|pr|issue|file}` checks an agent's citation in one command. Exit 0 means verified.
- **Read-only by default**: anything mutating (cron edits, agent adds, skill installs) is gated behind the explicit `clawctl cli` subcommand, never an HTTP shortcut.

If you run a self-hosted openclaw fleet and you've ever wanted "the kubectl of agents," this is that.

## Install

### Homebrew (recommended)

```bash
brew tap tomstagl/clawctl
brew install oc
```

### curl

```bash
curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash
```

The installer detects your OS + architecture (`darwin-arm64`, `darwin-amd64`, `linux-amd64`, `linux-arm64`), downloads the matching binary from the latest GitHub release, verifies it against the published `SHA256SUMS`, and drops it at `/usr/local/bin/clawctl` (configurable via `CLAWCTL_INSTALL_DIR`). It refuses to overwrite anything at the target path that does not respond to `clawctl version`, so an existing alias to `kubectl`/OpenShift's `oc` won't get clobbered.

To pin a specific tag instead of `latest`:

```bash
CLAWCTL_VERSION=v1.2.3 curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash
```

The installer does not touch your Keychain — you store the bearer token yourself (see below).

### From source

```bash
git clone https://github.com/tomstagl/clawctl.git
cd clawctl
go build -o /usr/local/bin/clawctl ./cmd/clawctl
```

### SSH connection reuse (recommended for `clawctl cli`)

`clawctl cli` shells out over SSH on every invocation. Without connection multiplexing, every call pays the TCP+auth handshake — typically 500–800ms before any work happens. The repo ships an ssh-config snippet that enables ControlMaster reuse so the second and subsequent calls within 10 minutes reuse the master connection.

```bash
./install/ssh-setup.sh
```

What it does:

- Appends a fenced `# BEGIN clawctl` / `# END clawctl` block to `~/.ssh/config` containing `ControlMaster auto`, `ControlPath ~/.ssh/cm-%r@%h:%p`, and `ControlPersist 10m`.
- Idempotent: re-running replaces the block in place, never duplicates it. Pre-existing config outside the markers is left alone.
- Creates `~/.ssh` with mode `0700` and the config with mode `0600` if missing.

Inspect [`install/ssh-config.snippet`](install/ssh-config.snippet) before running if you already manage `ControlMaster` yourself — your existing settings win precedence by being earlier in the file, but you may prefer to merge by hand.

### oc-remote (required for `clawctl cli`)

`clawctl cli` will refuse to run unless `/usr/local/bin/oc-remote` is present on the host. `oc-remote` is a tiny host-side shim that receives argv as a slice and exec's `openclaw "$@"`, so callers cannot inject host-side shell metacharacters via argv. The wrapper does **not** ship a `printf %q`-into-shell-string fallback — there is no second path.

Install on the gateway host (one-shot, idempotent):

```bash
ssh "$CLAWCTL_SSH_HOST" 'sudo install -m 0755 /dev/stdin /usr/local/bin/oc-remote' <<'OCREMOTE'
#!/usr/bin/env bash
set -euo pipefail
export PATH="$HOME/.npm-global/bin:$PATH"
exec openclaw "$@"
OCREMOTE
```

Verify:

```bash
ssh "$CLAWCTL_SSH_HOST" 'test -x /usr/local/bin/oc-remote && echo ok'
clawctl cli help
```

If you skip this step, `clawctl cli` exits 2 with a remediation message pointing back here. The HTTP-API subcommands (`health`, `models`, `msg`, `stream`, `raw`, `verify`, `trace`) do not require `oc-remote`.

## Setup

One required environment variable. Export it in `~/.zshrc` (or equivalent):

```bash
export CLAWCTL_HOST="http://your-openclaw-host:18789"
```

If you plan to use `clawctl cli`, also set `CLAWCTL_SSH_HOST` to whatever you already use to `ssh` to the gateway — an alias from `~/.ssh/config`, an FQDN, or a `user@host` pair:

```bash
export CLAWCTL_SSH_HOST="ops@your-openclaw-host"   # only needed for `clawctl cli`
```

Optional knobs — sensible defaults exist:

| Variable                | Default                         | Purpose                                                  |
| ----------------------- | ------------------------------- | -------------------------------------------------------- |
| `CLAWCTL_HOST`               | _required_                      | Gateway URL                                              |
| `CLAWCTL_SSH_HOST`           | _unset_                         | SSH target for `clawctl cli` (alias, FQDN, or `user@host`) |
| `CLAWCTL_KEYCHAIN_SERVICE`   | `openclaw-gateway-token`        | macOS Keychain entry holding the bearer token            |
| `CLAWCTL_TIMEOUT`            | `60`                            | Per-call timeout (seconds)                               |
| `CLAWCTL_CACHE_DIR`          | `~/.cache/clawctl`                   | Where the models cache + redaction audit live            |
| `CLAWCTL_MODELS_TTL`         | `60`                            | `clawctl models` cache TTL (seconds)                          |
| `CLAWCTL_NO_REDACT`          | `0`                             | Set to `1` to bypass redaction (debugging only)          |
| `CLAWCTL_JAEGER_UI`          | _unset_                         | Used by `clawctl trace` to print a working UI link            |

Store the token in Keychain once:

```bash
security add-generic-password \
  -s openclaw-gateway-token \
  -a "$USER" \
  -w "<your-bearer-token>"
```

Verify:

```bash
clawctl health
clawctl models | jq -r '.data[].id'
```

## Surface

| Command                                | Purpose                                              |
| -------------------------------------- | ---------------------------------------------------- |
| `clawctl health`                            | Gateway liveness (no auth)                           |
| `clawctl models`                            | List registered agents (60s cached)                  |
| `clawctl msg [-s SESSION] AGENT [TEXT]`     | One-shot chat; stdin if `TEXT` omitted               |
| `clawctl stream [-s SESSION] AGENT [TEXT]`  | Same, SSE-buffered + redacted                        |
| `clawctl raw METHOD PATH [curl-args]`       | Arbitrary `/v1/...` call with auth + traceparent     |
| `clawctl cli SUBCOMMAND...`                 | Run `openclaw …` over SSH on the gateway host        |
| `clawctl verify KIND ARGS`                  | Claim verification — `commit`, `pr`, `issue`, `file` |
| `clawctl trace TRACE-ID`                    | Print Jaeger UI link + first 30 spans for a trace    |

Exit codes: `0` ok, `2` usage, `6` DNS, `7` connection refused, `22` HTTP 4xx/5xx, `28` timeout.

## Recipes

The most common workflows live in [`docs/recipes.md`](docs/recipes.md). Highlights:

- _"Is the gateway alive and what's it serving?"_
- _"Talk to a specific agent and capture the trace-id."_
- _"Schedule a daily brief at 07:00 local time."_
- _"Investigate a stuck or failed run."_
- _"Rotate the gateway token without dropping traffic."_

A full openclaw CLI reference lives at [`docs/cli-reference.md`](docs/cli-reference.md).

## Use with Claude Code

This repo doubles as a Claude Code plugin and an MCP server. Once `clawctl` is on PATH, you have two integrations to choose from (or use both):

- **MCP server** — register `clawctl mcp` and Claude Code (or any MCP client) can call openclaw agents as typed tools. One tool per agent in `/v1/models`, traced and redacted at the boundary. See [`docs/mcp.md`](docs/mcp.md) for the one-line `claude mcp add` invocation, env requirements, and a worked `tools/list` + `tools/call` example.
- **Slash-command plugin** — install from the same repo:

```
/plugin marketplace add tomstagl/clawctl
/plugin install clawctl
```

What ships with the plugin:

| Surface                | Name                | Purpose                                                                                  |
| ---------------------- | ------------------- | ---------------------------------------------------------------------------------------- |
| Slash command          | `/clawctl`          | Drive openclaw — health, models, msg, cli, verify, trace.                                |
| Slash command          | `/clawctl-recipes`  | Curated workflows (cron, sessions, debugging, redaction recovery, token rotation).       |
| Slash command          | `/clawctl-cli`      | Full openclaw CLI reference (every subcommand + flags + examples).                       |
| Skill                  | `openclaw-loopback` | Convention for openclaw agents that deliver work via GitHub: labels, YAML deliverable header, R-1..R-12 communication-layer rules, new-agent checklist. |

The slash commands enforce the same five rules as the CLI from the Claude side: read-only by default, JSON-first, trace every call, redact at the boundary, never bypass the wrapper.

## Design principles

These are non-negotiable. PRs that violate them will be rejected.

1. **Read-only by default.** Mutating commands must be explicit, named, and never invoked autonomously.
2. **No secrets on disk.** Tokens live in Keychain. Env-only fallback is forbidden — `clawctl` would rather fail than read a token from a dotfile.
3. **Trace every call.** A traceparent is generated per invocation and printed to stderr. Reporting an issue means citing a trace-id, not a body.
4. **Redact at the boundary.** Even if upstream agents leak, the wrapper masks before output ever reaches the terminal. The audit log is append-only.
5. **One binary, zero runtime deps.** Bash + `curl` + `jq` (optional) + `security` (macOS) + `openssl`. That's it.

## Contributing

Bug reports and PRs welcome. Before opening a PR:

1. Read `docs/design-principles.md`.
2. Test against your own openclaw gateway (`clawctl health` should still pass).
3. Add or update a recipe in `docs/recipes.md` if you introduce a new pattern.

## License

MIT — see [LICENSE](LICENSE).
