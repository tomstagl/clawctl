# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Two products in one tree:

1. **`clawctl`** — a single Bash file (`./clawctl`) that wraps the openclaw gateway's OpenAI-compatible HTTP API plus a host-side ops CLI (`openclaw …` over SSH). All transport, auth, tracing, redaction, and verification logic lives in this one script. There is no build step.
2. **A Claude Code plugin** (`.claude-plugin/plugin.json`) that ships slash commands in `commands/` and a skill in `skills/openclaw-loopback/` enforcing the same rules from the Claude side. Both are distributed from this repo.

The wrapper and plugin share a single source of truth: the design principles in `docs/design-principles.md`. Treat those as load-bearing — the rest of the codebase is shaped to satisfy them.

## Common commands

```bash
# Syntax check (matches CI)
bash -n ./clawctl
bash -n ./install/install.sh

# Lint (CI uses ludeeus/action-shellcheck @ severity=warning, ignoring test/)
shellcheck ./clawctl ./install/install.sh

# Smoke tests against a real gateway — requires CLAWCTL_HOST and a Keychain token
./test/smoke.sh

# Local install (copies ./clawctl → ~/.local/bin/clawctl)
./install/install.sh
```

There is no unit-test framework. `test/smoke.sh` is the entire test suite and is intentionally an end-to-end check: it requires a live gateway and a real Keychain entry. CI only runs `shellcheck` and `bash -n` (see `.github/workflows/ci.yml`); it does **not** run `smoke.sh` because there's no gateway in CI.

## Architecture

### `clawctl` script structure

The script is a `case "$cmd"` dispatcher (around line 217) with five top-level subcommands: `health`, `models`, `msg`/`stream`, `raw`, `cli`, `verify`, `trace`. Above the dispatcher is a block of `_`-prefixed helpers that every subcommand composes:

- `_require_host` / `_require_ssh_host` — env-var guards, exit 2 on miss.
- `_token` — reads bearer from macOS Keychain via `security find-generic-password`. **Never** falls back to env or disk; this is enforced as design principle #2.
- `_traceparent` / `_trace_id_of` — generate W3C `traceparent` per call; trace-id is printed to stderr (principle #3). Every outbound HTTP call must attach this header.
- `_redact` — Perl-based regex masker on stdout. Patterns are inlined in the script (`dt0c01`, `dt0s16`, `gh[psoru]_*`, AWS AKID, JWT, Brave, plus the live gateway-token literal). On hit: prints stderr warning, appends to `~/.cache/clawctl/last-redaction`. `CLAWCTL_NO_REDACT=1` bypasses (debug only — never set in CI). All output that may contain agent text **must** be piped through `_redact` before reaching the terminal (principle #4).
- `_models_cache` / `_known_agents` / `_validate_agent` — 60s TTL file cache at `$CLAWCTL_CACHE_DIR/models.json`. Slug validation fails *open* with a stderr warning if the cache is unreachable, so the wrapper never blocks transport on a metadata failure.
- `_chat` — the canonical `/v1/chat/completions` caller. Builds the JSON payload with `jq -nc`, attaches auth + traceparent, and translates `curl` exit codes to the documented set (`6` DNS, `7` connection refused, `22` HTTP 4xx/5xx, `28` timeout — these are surfaced to users, don't remap them).

Mutating operations live exclusively under `clawctl cli`, which SSHes to `$CLAWCTL_SSH_HOST` and runs `openclaw …` there. Read-only HTTP shortcuts for mutating endpoints are forbidden (principle #1). If a feature would require one, it goes behind `clawctl cli` instead.

### Streaming path (`clawctl stream`)

SSE responses are buffered to a tempfile, parsed by an inline Python heredoc that extracts `choices[].delta.content`, and **then** piped through `_redact`. Buffering is deliberate: regex-based redaction across chunk boundaries is unsafe, so the wrapper trades latency for boundary-safe masking. Don't "optimize" this into a streaming redactor without solving the boundary problem.

### Plugin layer

`commands/clawctl.md`, `commands/clawctl-recipes.md`, `commands/clawctl-cli.md` are user-facing slash commands invoked from Claude Code. They shell out to the same `./clawctl` binary; they do **not** reimplement transport. The skill at `skills/openclaw-loopback/SKILL.md` documents the GitHub loop-back contract (label scheme, YAML deliverable header, R-1..R-12 rules) that openclaw agents themselves must follow when delivering work — it's a convention doc, not executable code.

When adding or changing CLI behavior, update both sides: the Bash subcommand **and** the matching slash command doc. They drift fast otherwise.

### Ralph runner

`ralph.sh` is a separate concern — a long-running agent loop conformant with the `ralph-skills` plugin (it reads `prd.json` and either `prompt.md` for amp or `CLAUDE.md` for claude). It is unrelated to the `clawctl` wrapper itself; don't conflate them.

## Conventions specific to this repo

- **No new runtime deps.** Hard set: `bash`, `curl`, `openssl`, `security` (macOS), `perl`. Optional: `jq`, `gh`, `python3` (for the SSE parser and `clawctl trace`). Adding anything else means a separate repo (principle #5).
- **Exit codes are part of the contract.** `0` ok, `2` usage, `6` DNS, `7` refused, `22` HTTP error, `28` timeout. Documented in `README.md` and `clawctl help` — keep them aligned.
- **Stderr vs stdout discipline.** Trace-ids, redaction warnings, error explanations → stderr. Response bodies and parsed content → stdout. This lets users pipe `clawctl … | jq …` without polluting stdin.
- **macOS-only by design.** Linux/Windows Keychain support is explicitly out of scope per `CONTRIBUTING.md`. Do not add `secret-tool` / `cmdkey` fallbacks to this repo.
- **PR checklist** (from `CONTRIBUTING.md`): if you add a subcommand, add a row to the surface table in `README.md` and a recipe in `docs/recipes.md`. If you touch redaction, add a case to `test/smoke.sh`.

## Naming caveat

The binary name `clawctl` collides with `kubectl`'s alias on some setups, and the README also references `oc` / `ocw` aliasing. The script is self-contained, so renaming the file at install time is safe — but if you add features that hard-code the name, prefer `${0##*/}` or `$BASH_SOURCE` over the literal string `clawctl`.
