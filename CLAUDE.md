# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

`clawctl` is a single-file bash wrapper around the openclaw gateway (an OpenAI-compatible HTTP API + ops CLI on a remote host). The repo *also* ships as a Claude Code plugin from the same checkout: the wrapper is the binary, and `.claude-plugin/`, `commands/`, and `skills/` make it installable via `/plugin install clawctl`.

There is essentially one source file: `clawctl` (root). Everything else is docs, plugin metadata, install scripts, or a smoke test. Treat the script as the source of truth — README/docs/slash commands describe it, they don't drive it.

## Commands

```bash
# Lint / syntax (what CI runs — see .github/workflows/ci.yml)
bash -n ./clawctl
bash -n ./install/install.sh
shellcheck --severity=warning clawctl install/install.sh   # ignores test/

# Smoke test against a real gateway (requires CLAWCTL_HOST + Keychain token)
./test/smoke.sh

# Local install (idempotent — copies clawctl into ~/.local/bin)
./install/install.sh
```

There is no build step, no test framework, no package manager. `test/smoke.sh` is the only test and it skips cleanly when `CLAWCTL_HOST` is unset.

## Architecture

`clawctl` is a flat dispatcher: a `case "$cmd"` block at the bottom routes subcommands to inline handlers. Above it sits a small private helper layer (functions prefixed `_`) that every handler composes from:

- `_require_host` / `_require_ssh_host` — env var preconditions; exit 2 on miss.
- `_token` — `security find-generic-password` against `CLAWCTL_KEYCHAIN_SERVICE`. Called per-invocation; never cached, never written.
- `_traceparent` / `_trace_id_of` — generates a fresh W3C `traceparent` per HTTP call and extracts the 32-hex trace-id; the trace-id is echoed to **stderr** so users can cite it.
- `_redact` — boundary redactor implemented in inline `perl`. Reads stdin → stdout, masks known patterns (`dt0c01.*`, `dt0s16.*`, `gh[psoru]_*`, `AKIA…`, JWTs, `BSA…`, the literal gateway token from Keychain), warns to stderr, and appends to `$CLAWCTL_CACHE_DIR/last-redaction`. **All terminal-bound output flows through this** — bypassing it (e.g. by adding a new subcommand that prints HTTP bodies directly) violates design principle #4.
- `_models_cache` / `_known_agents` / `_validate_agent` — file-cached `/v1/models` lookup at `$CLAWCTL_CACHE_DIR/models.json` with `CLAWCTL_MODELS_TTL` (default 60s). Validation fails *open* with a stderr warning if the cache is unavailable.
- `_chat` — the `/v1/chat/completions` call used by both `msg` and `stream`. Builds the JSON payload with `jq -n`, attaches auth + traceparent, and maps `curl` exit codes to user-facing exit codes (6 DNS, 7 refused, 22 HTTP error, 28 timeout). HTTP error bodies are run through `_explain_http_error` to surface the OpenAI-style `.error.message`.

Two non-obvious dispatch shapes:

- **`stream`** buffers the whole SSE response to a temp file, then a small inline Python program reassembles `choices[0].delta.content` chunks before piping to `_redact`. This is intentional — redaction must see the full text, so streaming-to-terminal is not actually streamed. Don't "fix" this by piping live.
- **`cli`** shells out via `ssh` to `$CLAWCTL_SSH_HOST`. If `/usr/local/bin/oc-remote` exists on the host it's preferred (clean argv); otherwise each arg is `printf %q`-escaped into a single shell string. This is the only mutating surface — every other subcommand is read-only.

`verify` is offline/local: `commit` calls `git cat-file -t`, `pr`/`issue` call `gh ... view`, `file` checks the working tree (or a git ref). No HTTP, no traceparent — it's a claim-checker, not a gateway call.

## Plugin shell

`.claude-plugin/plugin.json` declares the plugin. Three slash commands in `commands/` (`clawctl`, `clawctl-recipes`, `clawctl-cli`) are user-facing entrypoints that wrap the binary. One skill in `skills/openclaw-loopback/` documents the GitHub-loop-back conventions for openclaw agents (label scheme, YAML deliverable header, R-1..R-12 rules). These files are markdown with frontmatter — editing them does not change the binary's behavior.

## Design principles (non-negotiable)

These are documented in `docs/design-principles.md` and they gate PR acceptance. Internalize them before changing behavior:

1. **Read-only by default.** Mutations live behind `clawctl cli` only. No `--force`, no convenience aliases for destructive ops.
2. **No secrets on disk.** Tokens come from Keychain at call time. Env-var fallback is forbidden — failing closed beats reading a token from a dotfile.
3. **Trace every call.** Every HTTP call gets a `traceparent`; the trace-id goes to stderr. If you add a new HTTP-emitting handler, it must do this.
4. **Redact at the boundary.** All terminal output passes through `_redact`. New patterns go into the perl `%pat` table in `_redact` *and* a corresponding case in `test/smoke.sh`.
5. **One binary, zero runtime deps.** Allowed: `bash`, `curl`, `openssl`, `security` (macOS), `perl` (system), optional `jq` and `gh`. No npm, no Python venv, no Homebrew-only deps. Heavier features belong in a separate repo.

PRs that violate these will be closed. Don't refactor them away.

## Releasing

1. Bump the version in the script header comment in `clawctl`.
2. `git tag v0.X.0 && git push --tags`.
3. Update `install/clawctl.rb` `url` and `sha256` for the new tag.

Note: `install/clawctl.rb` currently has `bin.install "oc"` but the binary is named `clawctl` — this is a stale formula and should be fixed before the next release.
