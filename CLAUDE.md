# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Two products in one tree:

1. **`clawctl`** — a typed Go binary built from `cmd/clawctl/` that wraps the openclaw gateway (OpenAI-compatible HTTP API) plus a host-side ops CLI (`openclaw …` over SSH). All transport, auth, tracing, redaction, verification, and the MCP stdio server live in this binary. Single static binary (`CGO_ENABLED=0`); no runtime deps beyond `ssh` (only for `clawctl cli`) and the macOS `security` tool (for Keychain).
2. **A Claude Code plugin** (`.claude-plugin/plugin.json`) that ships slash commands in `commands/` and a skill in `skills/openclaw-loopback/` enforcing the same rules from the Claude side. Both are distributed from this repo.

The wrapper and plugin share a single source of truth: the design principles in `docs/design-principles.md`. Treat those as load-bearing — the rest of the codebase is shaped to satisfy them.

`clawctl.bash` is the **deprecated** original Bash implementation, kept for parity testing (see `test/parity-*.sh`). Don't add features to it; port to Go.

## Common commands

```bash
# Compile + test the Go binary (matches CI)
go vet ./...
go test ./...
go test -run=^$ -fuzz=FuzzParseSSE -fuzztime=5s ./internal/sse/

# Build a local binary
go build -o ./clawctl ./cmd/clawctl

# Lint the bash scripts (clawctl.bash + install.sh) — CI uses ludeeus/action-shellcheck @ severity=warning, ignoring test/
bash -n ./clawctl.bash ./install/install.sh
shellcheck ./clawctl.bash ./install/install.sh

# Smoke + parity tests against a real gateway — requires CLAWCTL_HOST and a Keychain token
./test/smoke.sh

# Local install (downloads released binary; re-run upgrades in place)
./install/install.sh
```

For the full test landscape — what each script covers, which CI job runs it, what env it needs, and whether it targets a mock or a live gateway — see **[test/README.md](test/README.md)**.

## Architecture

### Entry point and dispatch

`cmd/clawctl/main.go` holds a small `switch cmd` dispatcher with these top-level subcommands: `health`, `models`, `msg`, `stream`, `raw`, `cli`, `verify`, `trace`, `mcp`, plus the hidden `_redact` surface. Each subcommand has its own file (`health.go`, `msg.go`, …) and matching `*_test.go`. Build version/commit are stamped via `-ldflags '-X main.version=… -X main.commit=…'` by `release.yml`.

### Internal packages

The cmd layer is thin — almost all logic lives under `internal/`:

- `internal/config` — loads `CLAWCTL_*` env vars into a `Config` struct.
- `internal/keychain` — the only token source. Shells out to `security find-generic-password -w`. **Never** falls back to env or disk (design principle #2). macOS-only.
- `internal/trace` — generates W3C `traceparent` per call; trace-id printed to stderr (principle #3). Every outbound HTTP call attaches this header.
- `internal/redact` — regex masker for known token formats (`dt0c01`, `dt0s16`, `gh[psoru]_*`, AWS AKID, JWT, Brave, plus the live gateway-token literal). On hit: prints stderr warning, appends to `~/.cache/clawctl/last-redaction`. `CLAWCTL_NO_REDACT=1` bypasses (debug only — never in CI). All output that may contain agent text **must** pass through this package before reaching stdout (principle #4).
- `internal/cache` — 60s TTL file cache for `/v1/models` at `$CLAWCTL_CACHE_DIR/models.json`. Slug validation fails *open* with a stderr warning if the cache is unreachable, so the binary never blocks transport on metadata.
- `internal/transport/api` — the canonical authenticated HTTP client. Wraps `net/http` with bearer auth from a `TokenSource`, traceparent injection, and curl-aligned typed errors that the cmd layer maps onto the exit-code contract (`6` DNS, `7` refused, `22` HTTP 4xx/5xx, `28` timeout).
- `internal/sse` — Server-Sent Events parser for streaming chat completions. Has a fuzz target (`FuzzParseSSE`) that CI runs for 5s on every PR.
- `internal/envelope` — typed v1 ToolResponse / ToolStreamChunk envelopes that `msg`/`stream` emit by default. `--text` flag toggles to plain content.
- `internal/logging` — stderr formatting; `CLAWCTL_LOG=json` switches to one JSON line per call.
- `internal/mcpserver` — MCP stdio server backing `clawctl mcp`. Exposes one tool per agent, registered via `claude mcp add clawctl --command clawctl --args mcp`.

Mutating operations live exclusively under `clawctl cli`, which SSHes to `$CLAWCTL_SSH_HOST` and runs `openclaw …` there. Read-only HTTP shortcuts for mutating endpoints are forbidden (principle #1). If a feature would require one, it goes behind `clawctl cli` instead.

### Streaming path (`clawctl stream`)

SSE responses flow through `internal/sse` chunk-by-chunk into the v1 stream envelope. Redaction still operates on **buffered, line-aligned** content rather than raw bytes — regex-based redaction across chunk boundaries is unsafe, so the implementation trades some latency for boundary-safe masking. Don't "optimize" this into a streaming byte redactor without solving the boundary problem; the fuzz target on the parser exists to catch related regressions.

### Plugin layer

`commands/clawctl.md`, `commands/clawctl-recipes.md`, `commands/clawctl-cli.md` are user-facing slash commands invoked from Claude Code. They shell out to the same `clawctl` binary; they do **not** reimplement transport. The skill at `skills/openclaw-loopback/SKILL.md` documents the GitHub loop-back contract (label scheme, YAML deliverable header, R-1..R-12 rules) that openclaw agents themselves must follow when delivering work — it's a convention doc, not executable code.

When adding or changing CLI behavior, update both sides: the Go subcommand **and** the matching slash command doc. They drift fast otherwise.

### Ralph runner

`ralph.sh` is a separate concern — a long-running agent loop conformant with the `ralph-skills` plugin (it reads `prd.json` and either `prompt.md` for amp or `CLAUDE.md` for claude). It is unrelated to the `clawctl` binary itself; don't conflate them.

### Distribution

`.github/workflows/release.yml` builds four binaries on every `v*` tag (`darwin/{arm64,amd64}`, `linux/{arm64,amd64}`), generates `SHA256SUMS`, and creates a GH Release. `install/install.sh` is the user-facing resolver: it detects the platform, downloads the matching binary + sums, verifies the checksum, and installs to `/usr/local/bin/clawctl` (sudo-promoting if needed). It refuses to overwrite a binary at the target path that doesn't respond to `clawctl version` with a string containing `clawctl` — that's the install sentinel, so any rewrite of the `version` subcommand must preserve it.

## Conventions specific to this repo

- **Static Go binary, no CGO.** `CGO_ENABLED=0` everywhere. Don't add libraries that require CGO; for OS integrations (Keychain, future Linux secret backends), shell out instead.
- **Exit codes are part of the contract.** `0` ok, `2` usage, `6` DNS, `7` refused, `22` HTTP error, `28` timeout. Documented in `README.md`, `clawctl --help`, and `docs/cli-reference.md` — keep them aligned.
- **Stderr vs stdout discipline.** Trace-ids, redaction warnings, error explanations, JSON logs (`CLAWCTL_LOG=json`) → stderr. Response bodies, parsed content, envelopes → stdout. This lets users pipe `clawctl … | jq …` without polluting stdin.
- **macOS-only by design (today).** `internal/keychain` shells to `security`. Linux secret backends are planned (see `tasks/prd-clawctl-distribution-and-integration.md` US-003) but not yet present — don't assume they exist.
- **Parity tests are load-bearing.** `test/parity-*.sh` and `test/envelope-*.sh` compare the Go binary against `clawctl.bash` for every read-only subcommand. While `clawctl.bash` is still in-tree, any change to a Go subcommand's user-facing behavior should keep those scripts passing — or update them with a matching change to the bash file when the divergence is intentional.
- **PR checklist** (from `CONTRIBUTING.md`): if you add a subcommand, add a row to the surface table in `README.md` and a recipe in `docs/recipes.md`. If you touch redaction, add a case to `test/smoke.sh` and a `redact_test.go` case.

## Naming caveat

The binary name `clawctl` collides with `kubectl`'s alias on some setups, and the README also references `oc` / `ocw` aliasing. The binary is self-contained, so renaming at install time is safe. If you add features that need to identify the binary by name, prefer `os.Args[0]` over the literal string `clawctl`.
