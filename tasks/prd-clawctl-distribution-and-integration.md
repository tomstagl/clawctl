# PRD: clawctl Distribution & Agentic Workspace Integration

## 1. Introduction / Overview

`clawctl` (Go rewrite, single static binary) and its Claude Code plugin already exist in this repo, with a working release workflow and `install/install.sh` resolver. This PRD captures the work to **finish productizing the distribution path** so that:

1. Any developer on macOS or Linux can install a verified `clawctl` binary in one command.
2. Any agentic engineering workspace — Claude Code, an MCP-aware client, or a generic shell-driven agent (Cursor, Aider, plain bash) — can call `clawctl` reliably with stable, machine-readable output.
3. The Claude Code plugin in this repo is discoverable through the plugin marketplace, not just `git clone`.

This PRD does **not** add Homebrew, a tap, or per-tool integration shims. The decision is to invest in three channels — `install.sh`, the Claude Code plugin marketplace, and an in-binary MCP server — and harden them, rather than spreading thin across many.

## 2. Goals

- A signed, checksum-verified `curl … | bash` install path for macOS arm64/amd64 and Linux arm64/amd64.
- Linux is a first-class target: the binary works without macOS Keychain, via a portable secret backend.
- The release workflow is exercised end-to-end at least once and proven by an actual `v1.0.0` tag.
- The repo's Claude Code plugin is registered in a marketplace manifest so users can install it without cloning.
- `clawctl` exposes a stable, documented machine-readable surface (`--json` mode, frozen exit codes, schema'd error envelope) that any agent can drive.
- `clawctl mcp` runs an MCP stdio server exposing read-only operations, registerable via `claude mcp add`.
- One-line install works behind `curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash` (no custom domain required).

## 3. User Stories

### US-001: Verify release workflow end-to-end with a v0.x dry run
**Description:** As a maintainer, I want to confirm that `release.yml` actually produces working artifacts before tagging v1.0.0.

**Acceptance Criteria:**
- [ ] Tag `v0.9.0-rc1` (or similar pre-1.0 tag) is pushed and the workflow completes green.
- [ ] All four artifacts (`clawctl-darwin-arm64`, `clawctl-darwin-amd64`, `clawctl-linux-arm64`, `clawctl-linux-amd64`) appear on the GH Release.
- [ ] `SHA256SUMS` is present and `shasum -a 256 -c` validates each artifact locally.
- [ ] `actionlint` and `go test` both ran and passed in the workflow.
- [ ] If anything fails, fix the workflow and re-run before opening any other story in this PRD.

### US-002: install.sh post-release smoke test in CI
**Description:** As a maintainer, I want CI to prove that `install/install.sh` can install a real release artifact, so a regression in the resolver is caught before a user hits it.

**Acceptance Criteria:**
- [ ] Add a job to `.github/workflows/ci.yml` (matrix: macos-latest, ubuntu-latest) that runs `install/install.sh` against the latest published release.
- [ ] Job pins `CLAWCTL_INSTALL_DIR` to a tempdir (no sudo needed) and asserts `$INSTALL_DIR/clawctl version` exits 0 and contains `clawctl`.
- [ ] Job runs `clawctl --help` and asserts exit 0.
- [ ] `bash -n install/install.sh` and `shellcheck install/install.sh` already pass (regression check).

### US-003: Portable secret backend for Linux (`CLAWCTL_TOKEN_CMD`)
**Description:** As a Linux user, I want to authenticate without macOS Keychain, so I can use the same binary on a Linux dev box or CI runner.

**Acceptance Criteria:**
- [ ] On Linux, the token resolver tries (in order): `CLAWCTL_TOKEN_CMD` (shells out, captures stdout), `secret-tool lookup service openclaw-gateway-token account $USER`, `pass show openclaw/gateway-token`, then fails with a precise error listing the three options.
- [ ] On macOS, existing Keychain path is unchanged; `CLAWCTL_TOKEN_CMD` is honored if set (for users who want to override).
- [ ] `CLAWCTL_TOKEN` env var is **not** added — design principle #2 forbids on-disk/env tokens. The shell-out is acceptable because the user controls the command.
- [ ] `docs/auth.md` (new) documents all four backends with a one-paragraph setup recipe each.
- [ ] Unit tests cover: macOS path, Linux with `CLAWCTL_TOKEN_CMD`, Linux with no backend (clean error), and explicit-override-on-mac.

### US-004: Linux smoke test in CI
**Description:** As a maintainer, I want `test/smoke.sh` to run on Linux too, so the Linux build doesn't silently regress.

**Acceptance Criteria:**
- [ ] Add a Linux job to `ci.yml` that runs `bash -n test/smoke.sh` and `shellcheck test/smoke.sh`.
- [ ] Add a `--no-network` (or equivalent) flag to `test/smoke.sh` that skips any test requiring a live gateway, and assert it exits 0 on both Linux and macOS.
- [ ] Document in `CONTRIBUTING.md` how to run the full live smoke test locally.

### US-005: Stable `--json` output mode across read commands
**Description:** As an agent author, I want `clawctl health`, `models`, `msg`, `verify`, and `trace` to emit a stable JSON envelope, so I can parse output without scraping prose.

**Acceptance Criteria:**
- [ ] `--json` global flag (or `CLAWCTL_OUTPUT=json` env) emits one JSON object per command invocation.
- [ ] Envelope shape (frozen as v1): `{"command": "...", "ok": true|false, "data": {...}, "error": null | {"code": "...", "message": "...", "trace_id": "..."}}`.
- [ ] `data` shape per command is documented in `docs/cli-contract.md` with an example for each.
- [ ] When `--json` is set, redaction still applies to `data` and `error.message`.
- [ ] Trace-id is also written to stderr (existing behavior preserved) so log-tailing tools still see it.
- [ ] Unit tests assert the envelope shape for each command.

### US-006: Document and freeze the exit-code contract
**Description:** As an agent author, I want exit codes I can branch on without reverse-engineering them.

**Acceptance Criteria:**
- [ ] `docs/cli-contract.md` lists all exit codes (`0`, `2`, `6`, `7`, `22`, `28`) with the precise condition that emits each.
- [ ] `clawctl --help` includes a one-line pointer to `docs/cli-contract.md`.
- [ ] An end-to-end test asserts each documented exit code is actually emitted (mock a 4xx, kill the connection, etc.).
- [ ] README "Exit codes" table matches `docs/cli-contract.md` (single source — link, don't duplicate).

### US-007: Register the Claude Code plugin in a marketplace manifest
**Description:** As a Claude Code user, I want to install the clawctl plugin without cloning the repo.

**Acceptance Criteria:**
- [ ] Add `.claude-plugin/marketplace.json` (or follow whatever filename the marketplace currently expects — check current Claude Code docs at install time) listing this repo as a plugin source.
- [ ] Plugin `version` in `.claude-plugin/plugin.json` is bumped to track the binary release tag (sync to `v1.0.0`).
- [ ] `README.md` adds a "Install from Claude Code" section with the exact `/plugin` or `claude plugin install` command.
- [ ] If the plugin requires the `clawctl` binary at runtime, the plugin's first-run path detects its absence and prints the `install.sh` one-liner — no silent failures.

### US-008: `clawctl mcp` — read-only MCP stdio server
**Description:** As a user of any MCP-aware client (Claude Desktop, Cursor, Continue), I want to expose clawctl's read-only ops as MCP tools without writing any glue code.

**Acceptance Criteria:**
- [ ] `clawctl mcp` starts an MCP stdio server.
- [ ] Exposes tools: `health`, `models`, `msg` (non-streaming), `verify`, `trace` — read-only only. `cli` (mutating) is **not** exposed.
- [ ] Each tool's input schema is documented and matches the corresponding subcommand's flags.
- [ ] Tool output goes through `_redact` before being returned to the client.
- [ ] `docs/mcp.md` includes a paste-ready `claude mcp add clawctl -- clawctl mcp` registration line and equivalent for Claude Desktop's `claude_desktop_config.json`.
- [ ] Unit tests cover: server startup, one tool round-trip per exposed tool, and that `cli` is genuinely absent from the tool list.

### US-009: `clawctl mcp` — guard rails for mutating ops
**Description:** As a security-conscious user, I want a clear, opt-in story for ever exposing `cli` over MCP — not a silent flag.

**Acceptance Criteria:**
- [ ] No flag exposes `cli` over MCP in v1.0. Document explicitly in `docs/mcp.md` why ("mutating SSH-driven ops require a human in the loop").
- [ ] If a future story changes this, it must require an explicit `--unsafe-mutating` flag and print a stderr warning at server start.

### US-010: Bootstrap helper (`clawctl init`)
**Description:** As a new user, I want one command that prints the env-var exports and Keychain/secret-backend setup commands tailored to my platform.

**Acceptance Criteria:**
- [ ] `clawctl init` detects OS, prints platform-correct setup snippets (Keychain on mac, `secret-tool` / `pass` / `CLAWCTL_TOKEN_CMD` on Linux).
- [ ] Output is plain stdout (no colors when `!isatty`), copy-pasteable.
- [ ] `clawctl init --check` validates that `CLAWCTL_HOST` is set, the token resolver works, and `clawctl health` returns 200 — exits 0 only if all three pass.
- [ ] Unit tests cover each platform branch.

### US-011: Fix and tag the Homebrew formula stub OR delete it
**Description:** The current `install/clawctl.rb` is broken (`bin.install "oc"` references a file that no longer exists). Decide its fate before v1.0 to avoid shipping a broken artifact.

**Acceptance Criteria:**
- [ ] **Decision:** Since Homebrew is **not** a chosen distribution channel for v1.0, delete `install/clawctl.rb` to remove the misleading file.
- [ ] If a tap is added later, it gets its own repo (`tomstagl/homebrew-clawctl`) and is tracked under a different PRD.

### US-012: README install-first rewrite
**Description:** As a first-time visitor, I want the install command to be in the first 20 lines of the README, not buried.

**Acceptance Criteria:**
- [ ] First section after the title is "Install" with the `curl … | bash` one-liner using the canonical `tomstagl/clawctl` repo URL.
- [ ] Second section is "Quickstart" with three commands (`clawctl init`, `clawctl health`, one example `clawctl msg`).
- [ ] Move existing detail (architecture, design principles, surface table) below.
- [ ] Add a "Use from agents" section linking to `docs/cli-contract.md` and `docs/mcp.md`.

### US-013: Tag v1.0.0 and validate the full path
**Description:** As a maintainer, I want a real v1.0.0 release that I can demonstrate with a clean machine.

**Acceptance Criteria:**
- [ ] Tag `v1.0.0`, workflow completes green, all four artifacts published with `SHA256SUMS`.
- [ ] On a fresh macOS box (or a clean container for Linux): `curl -fsSL https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh | bash` installs successfully.
- [ ] `clawctl version` reports `v1.0.0`.
- [ ] `clawctl init` prints platform-correct setup; `clawctl health` returns 200 against a real gateway.
- [ ] Plugin install via marketplace works on a clean Claude Code session.
- [ ] `clawctl mcp` registers via `claude mcp add` and at least one tool round-trips.

## 4. Functional Requirements

- **FR-1:** Release workflow MUST produce four binaries (`darwin/{arm64,amd64}`, `linux/{arm64,amd64}`) plus `SHA256SUMS` for every `v*` tag.
- **FR-2:** `install/install.sh` MUST verify the downloaded binary against `SHA256SUMS` before installing.
- **FR-3:** On Linux, the token resolver MUST try `CLAWCTL_TOKEN_CMD`, `secret-tool`, `pass` in that order, and MUST NOT accept a plain env-var token.
- **FR-4:** Every read-only subcommand MUST support `--json` and emit the v1 envelope shape.
- **FR-5:** Exit codes `0/2/6/7/22/28` MUST be documented in `docs/cli-contract.md` and asserted by tests.
- **FR-6:** `clawctl mcp` MUST expose only read-only ops in v1.0.
- **FR-7:** `clawctl init` MUST detect platform and emit platform-correct setup snippets.
- **FR-8:** `.claude-plugin/marketplace.json` MUST be present and reference `tomstagl/clawctl`.
- **FR-9:** README MUST show the install command in the first 20 lines.
- **FR-10:** All output passing through `--json` MUST still be redacted.

## 5. Non-Goals (Out of Scope)

- **No Homebrew tap.** Decided: not a v1.0 channel. The broken formula stub is deleted (US-011).
- **No GUI installer.** No `.pkg`, no `.dmg`, no Windows MSI.
- **No Windows support.** Mac and Linux only. Windows is not on the roadmap and is not a build target.
- **No per-agent integration shims.** No Cursor rules file, no Aider command file, no Continue config helper. Other agents drive `clawctl` through its stable CLI surface.
- **No mutating MCP tools.** `cli` is explicitly excluded from v1.0 MCP server (US-009).
- **No telemetry, no auto-update.** Users update by re-running `install.sh` (which already upgrades in place).
- **No custom domain (e.g. `clawctl.dev`).** Install is via the raw GitHub URL; revisit only if discoverability becomes a problem.
- **No release signing (cosign / Sigstore / GPG).** SHA256SUMS + GH Releases trust is sufficient for v1.0. Listed in Open Questions for v1.x.

## 6. Design Considerations

- **Single source of truth for the CLI contract.** `docs/cli-contract.md` is referenced from README, `--help`, and any agent-facing docs. Don't duplicate exit codes or envelope shape.
- **MCP server reuses existing command implementations.** Don't fork the logic — `clawctl mcp` should call into the same internal packages that the CLI subcommands use, so behavior cannot drift.
- **Redaction stays server-side of the CLI/MCP boundary.** Both `--json` output and MCP tool responses pass through `redact.go` before leaving the process. Verified by tests.
- **`init` is read-only and idempotent.** Never writes to disk, never modifies shells. Print, don't apply.

## 7. Technical Considerations

- The Go binary is statically linked (`CGO_ENABLED=0`). Keep it that way — adding `libsecret` as a CGO dep would break the static-binary contract. Use `secret-tool` and `pass` as out-of-process subprocesses (matches the `CLAWCTL_TOKEN_CMD` pattern).
- The MCP server uses stdio (not HTTP). Adding HTTP would expand the threat model — out of scope.
- `release.yml` already pins `go-version: "1.24"`. Keep it pinned across this PRD; bump as a separate concern.
- `actions/upload-artifact@v4` retains release artifacts for 7 days. The release job downloads and re-uploads them to the GH Release, so this is fine for now — but the workflow should not be the long-term storage of binaries.
- `clawctl version` is the install.sh sentinel ("does this look like a clawctl?"). The version subcommand MUST continue to output a string containing the literal `clawctl`.

## 8. Success Metrics

- **Install success rate:** A new user can go from zero to `clawctl health` returning 200 in under 5 minutes on both macOS and Linux.
- **Agent integration friction:** A new agent author can wire up `clawctl --json msg …` in under 30 lines of shell or 50 lines of Python — verifiable by writing one such example in `docs/integrations/`.
- **MCP setup:** `claude mcp add clawctl -- clawctl mcp` followed by one round-trip works on a clean Claude Code install.
- **Release reliability:** Three consecutive `v*` tags ship green with no manual intervention.
- **No regressions:** `test/smoke.sh` (live gateway), `bash -n`, `shellcheck`, and `go test` all pass on every PR.

## 9. Open Questions

- **Release signing.** Is SHA256SUMS enough for v1.0, or do we want cosign / Sigstore / GPG? Defaulting to "no" for v1.0 — revisit if a downstream consumer requires it.
- **Versioning automation.** Manual `git tag v…` for now. Should we adopt `release-please` for changelogs in v1.1?
- **Linux secret backend default.** If a user has both `secret-tool` and `pass` installed, do we always prefer `secret-tool`? (Current design: yes. Override via `CLAWCTL_TOKEN_CMD`.)
- **MCP tool naming.** Use `clawctl_health` / `clawctl_models` / etc. (prefixed) or bare `health` / `models`? Prefixing avoids collisions when multiple MCP servers are registered — leaning toward prefixed.
- **`install.sh` short URL.** Acceptable to live at `https://raw.githubusercontent.com/tomstagl/clawctl/main/install/install.sh` indefinitely, or do we want a vanity domain? v1.0 ships with the raw URL.
- **Plugin marketplace mechanics.** The exact filename and shape of the marketplace manifest depend on Claude Code's current plugin spec — confirm at implementation time (US-007) rather than guessing now.
