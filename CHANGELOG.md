# Changelog

## v0.3.1

Documentation updates, release workflow hardening, and MCP server configuration support.

### Fixed

- fix(release): drop needless checkout from release job, extend artifact retention (#5)

### Changed

- chore: add Dynatrace MCP server config for CCR fleet leads (#6)
- Backfill v0.2.4 release artifacts and draft notes (issue #7) (#8)

### Internal

- docs: remove broken Homebrew install references (#10)

## v0.3.0

Robustness hardening, doc-drift fixes, and incremental alignment of the tool envelope with the A2A (Agent2Agent) standard.

### Robustness

- All HTTP response bodies are now bounded (`internal/transport/api.ReadLimited` + `CLAWCTL_MAX_RESPONSE_BYTES`, default 10 MiB). Oversized bodies map to exit 22 instead of exhausting memory; applied to the gateway client and both best-effort Jaeger fetches.
- The MCP `clawctl_msg` tool rejects oversized prompts before any network call.
- The Linux keychain backend distinguishes "tool not installed" from "tool ran but failed", surfacing the failing backend's stderr instead of a generic three-option hint.
- Malformed SSE `data:` payloads are counted and reported on stderr, so a corrupt stream is no longer indistinguishable from a healthy empty response.

### Incremental A2A alignment

- New additive v1 envelope field `task_id` (A2A `taskId`) on all envelope members. `msg`/`stream` derive it from the call's trace-id when omitted; the MCP `clawctl_msg` tool accepts and echoes it. Emitters still validate against the v1 schema.
- `/v1/models` parsing now reads `capabilities`/`skills` into a minimal agent card and surfaces them in the agent tool description for routing. Fails open when absent.
- New `docs/agent-protocol.md` records clawctl's protocol position: the three surfaces (MCP / inference / GitHub loop-back), the A2A↔clawctl concept map, and an SSH-side push-notification seam (design only).

### Documentation

- `docs/design-principles.md` (#2/#5) and `CLAUDE.md` now describe the static Go binary and the implemented Linux keychain backend (previously "one bash file / macOS-only").
- Clarified that "fails open" refers to the models cache layer, not MCP slug validation.
- Documented `CLAWCTL_MAX_RESPONSE_BYTES` and the single-deadline HTTP timeout behavior.

## v0.1.0

Initial production release of `clawctl` — a typed Go binary wrapping the openclaw gateway's OpenAI-compatible HTTP API plus a host-side ops CLI over SSH.

### Distribution

- `install/install.sh` resolves the correct platform binary from GitHub Releases, verifies SHA256, and installs to `/usr/local/bin/clawctl` (or `$CLAWCTL_INSTALL_DIR`). Supports darwin/{arm64,amd64} and linux/{arm64,amd64}.
- CI post-release smoke test (`install-smoke` job) proves install.sh works on both macOS and Linux runners against a real release artifact.
- README restructured: install curl one-liner in the first 20 lines, Quickstart section with three commands, and a "Use from agents" section linking to contract docs.

### Linux secret backend (`CLAWCTL_TOKEN_CMD`)

- On Linux, tokens are resolved in priority order: `CLAWCTL_TOKEN_CMD` (arbitrary shell command), `secret-tool lookup service openclaw-gateway-token account $USER`, `pass show openclaw/gateway-token`.
- `CLAWCTL_TOKEN_CMD` is also honoured on macOS as an override, taking priority over Keychain.
- `CLAWCTL_TOKEN` env var is intentionally **not** supported (design principle #2).
- Setup recipes documented in `docs/auth.md`.

### Stable `--json` output

- `--json` global flag (or `CLAWCTL_OUTPUT=json`) accepted by `health`, `models`, `msg`, `verify`, and `trace`.
- Each command emits a single JSON object: `{"command":"…","ok":true|false,"data":{…},"error":null|{…}}`.
- Redaction applied to `data` and `error.message` before output.
- Envelope shapes and examples documented in `docs/cli-contract.md`.

### Command-based MCP server

- `clawctl mcp` now exposes five typed MCP tools: `clawctl_health`, `clawctl_models`, `clawctl_verify`, `clawctl_trace`, `clawctl_msg`.
- No agent-per-tool dynamic registration; tools are statically declared at startup.
- `clawctl_msg` applies redaction before returning to the MCP client.
- `cli` subcommand is explicitly excluded from MCP (mutating SSH ops require a human in the loop). Documented in `docs/mcp.md`.
- Registration command: `claude mcp add clawctl --command clawctl --args mcp`.

### Bootstrap helper (`clawctl init`)

- `clawctl init` prints platform-correct setup snippets (Keychain command on macOS, `CLAWCTL_TOKEN_CMD`/`secret-tool`/`pass` on Linux). ANSI suppressed when stdout is not a tty.
- `clawctl init --check` validates the environment in three steps: `CLAWCTL_HOST` set, token resolver returns a non-empty token, `GET /health` returns HTTP 200. Exits 2 with per-check results on any failure.

### Exit-code contract

- Exit codes 0, 2, 6, 7, 22, 28 are documented in `docs/cli-contract.md` and linked from `clawctl --help`.
- Contract tests in `cmd/clawctl/exitcode_test.go` cover all five failure codes using a mock transport (no real network).

### Plugin

- `.claude-plugin/plugin.json` (v0.1.0) ships slash commands `/clawctl`, `/clawctl-recipes`, `/clawctl-cli`.
- `commands/clawctl.md` checks for the binary at invocation and prints the install one-liner if absent.
- Install: `claude plugin install tomstagl/clawctl` (or equivalent marketplace command).

### CI improvements

- Linux smoke test (`smoke-static` job) runs `bash -n` and `shellcheck` on `test/smoke.sh` on every PR.
- `test/smoke.sh` gains `--no-network` flag: skips all live-gateway tests when `CLAWCTL_HOST` is unset.
- Cross-compile matrix (`darwin/{arm64,amd64}`, `linux/{arm64,amd64}`) verified clean in `release.yml`.

## v0.2.4

Tagged 2026-05-23; never released — no GitHub release was published and no artifacts were built for this tag.

- The Claude Code plugin ships a `clawctl` skill (`skills/clawctl/SKILL.md`) covering every command, when to prefer HTTP over SSH, output formats, env vars, and exit codes; `install/install.sh` now also fetches the skill to `~/.claude/skills/clawctl/SKILL.md` and registers the `clawctl` MCP server at user scope.

## v0.2.3

- `clawctl cli`'s default `clawctl-remote` install path changed from `/usr/local/bin/clawctl-remote` to `~/.local/bin/clawctl-remote`, so the auto-install introduced in v0.2.1 no longer needs `sudo` on either macOS or Linux out of the box.

## v0.2.2

- `clawctl cli`'s auto-install of `clawctl-remote` (added in v0.2.1) no longer requires `sudo`: it creates the target directory first and installs with plain `install`, so a user-writable path set via `CLAWCTL_REMOTE_PATH` works without elevated privileges.

## v0.2.1

- `clawctl cli` now installs or updates `clawctl-remote` on the gateway host automatically over SSH when it is missing or out of date, removing the manual install step. The install path can be overridden with `CLAWCTL_REMOTE_PATH`.

## v0.2.0

Renames the SSH-side helper binary from `oc-remote` to `clawctl-remote` to match the wrapper's name; behavior is unchanged.

### Distribution

- `clawctl cli` now probes for and invokes `clawctl-remote` (formerly `oc-remote`) at `/usr/local/bin/clawctl-remote`; the missing-binary remediation message and install snippet in `README.md` were updated to match.
- README setup now leads with a `~/.config/clawctl/env` file convention for host-specific values (`CLAWCTL_HOST`, `CLAWCTL_SSH_HOST`, `CLAWCTL_JAEGER_UI`), sourced from the shell profile instead of committed.
- Fixed the documented MCP registration command, which referenced flags (`--command`/`--args`) that no longer exist; the correct form is `claude mcp add clawctl -- clawctl mcp`.
