# PRD: Full Test Coverage for clawctl

## Introduction

`clawctl` ships as a Go binary plus a Claude Code plugin (`commands/`, `skills/`). Today the Go side has 28 `*_test.go` files covering `internal/` and `cmd/`, and 13 shell scripts under `test/` covering parity, envelopes, exit codes, install resolution, and CLI hardening. **The gap is CI.** Only `go test`, `go vet`, the SSE fuzz target, `shellcheck`, and `bash -n` actually run on every PR. The shell scripts are well-architected — most already mock their dependencies (Python `http.server`, fake `curl`/`security`/`uname`) — but they aren't wired into CI. The plugin layer (`commands/*.md`, `skills/openclaw-loopback/`) has no automated tests at all. There is no Go coverage measurement and no nightly run against a real gateway.

There is also a **subtle dual-target problem**: only `test/smoke.sh` accepts a `BIN=` override. The other scripts (`envelope-*.sh`, `exit-codes.sh`, `cli-hardening.sh`) hardcode `BIN="$ROOT/clawctl.bash"`, and the parity scripts hardcode both `BASH_BIN` and `GO_BIN`. Wiring them into CI as-is would test the deprecated bash leftover, not the Go binary that ships. To test Go binary contract conformance through these scripts, they need a `BIN=` override (and the script content needs to not assume bash-specific behavior). Without that, `internal/envelope` and `cmd/clawctl/msg_test.go` remain the only Go-side validators of the v1 envelope contract.

This PRD scopes the work to (a) make the test suite we already have actually run, (b) make those scripts target the Go binary, (c) add the missing layers (plugin, coverage, real-gateway nightly), and (d) ensure release tags exercise the install + plugin paths end-to-end before binaries are cut.

## Goals

- Every existing shell script under `test/` runs on every PR (except `smoke.sh`, which requires a live gateway).
- Go test coverage is measured per package and a floor is enforced for `internal/`.
- A nightly workflow runs `smoke.sh` and any `*-real.sh` variants against a real openclaw gateway via repository secrets.
- The plugin layer (`commands/*.md`, `skills/openclaw-loopback/SKILL.md`) has automated structural and contract tests.
- On every `v*` tag, install.sh + plugin manifest are exercised end-to-end on macOS and Linux.
- Linux keychain backends (secret-tool, pass, CLAWCTL_TOKEN_CMD) are exercised in CI on a Linux runner.

## User Stories

### US-001: Wire mock-gateway parity scripts into CI
**Description:** As a maintainer, I want `test/parity-*.sh` to run on every PR so that bash↔Go behavioral drift is caught before merge.

**Acceptance Criteria:**
- [ ] New CI job `parity` (Ubuntu) runs `parity-health.sh`, `parity-models.sh`, `parity-raw.sh`, `parity-verify.sh`, `parity-redact.sh`, `parity-trace.sh`.
- [ ] Job installs `python3`, `jq`, and any other deps the scripts already declare.
- [ ] Each script's exit code surfaces as a separate CI step (so a single failure doesn't mask others).
- [ ] Job passes on a clean checkout of `main` before merging.
- [ ] `go vet` / `go test` / `shellcheck` jobs unaffected.

### US-002: Wire envelope contract scripts into CI
**Description:** As a maintainer, I want `test/envelope-*.sh` to run on every PR so that the v1 ToolResponse / ToolStreamChunk schema contract can't silently break.

**Acceptance Criteria:**
- [ ] New CI job `envelope` runs `envelope-msg.sh`, `envelope-stream.sh`, `envelope-redacted.sh`, `validate-fixtures.sh`.
- [ ] Job installs `npx` (for `ajv-cli`) and caches the npm install where reasonable.
- [ ] `schemas/envelope.v1.json` is the source of truth — tests fail if the binary's output doesn't validate.
- [ ] Job runs in under 2 minutes on the Ubuntu runner.

### US-003: Wire exit-codes + cli-hardening + install-resolver scripts into CI
**Description:** As a maintainer, I want the remaining mock-driven scripts to run on every PR so the documented exit-code contract and install resolver stay honest.

**Acceptance Criteria:**
- [ ] New CI job `contract` runs `exit-codes.sh`, `cli-hardening.sh`, `install-resolver.sh`.
- [ ] Each script invoked individually, with its own step.
- [ ] Failures produce a clear, scoped message (e.g. "exit-codes.sh: subcommand=health expected=22 got=0").

### US-004: Add Go coverage measurement and floor
**Description:** As a maintainer, I want every PR to report Go test coverage per package and fail if `internal/` drops below an agreed floor, so that regressions in the typed core can't slip through.

**Acceptance Criteria:**
- [ ] `go test -coverprofile=coverage.out -covermode=atomic ./...` replaces the existing `go test ./...` step.
- [ ] Per-package coverage is printed to the job log via `go tool cover -func=coverage.out`.
- [ ] A coverage floor of **65% per `internal/...` package** is enforced. Baseline measured 2026-05-10: `cache` 70.2%, `config` 96.2%, `envelope` 66.7%, `keychain` 85.2%, `logging` 92.7%, `mcpserver` 68.8%, `redact` 96.6%, `sse` 96.5%, `trace` 83.3%, `transport/api` 71.9%. The 65% floor catches regressions on the lowest-covered package (`envelope`) without forcing aspirational coverage work into this PRD's scope.
- [ ] Threshold is implemented as a small awk/grep step on `go tool cover -func` output in the workflow YAML — no new tooling, no separate config file.
- [ ] `coverage.out` is uploaded as a workflow artifact.
- [ ] No coverage requirement on `cmd/clawctl` (entry point dispatch is hard to cover meaningfully); the floor is internal-only.
- [ ] Raising the floor is explicitly out of scope here — separate PRD if/when the team wants to push it up.

### US-005: Run smoke.sh nightly against a real gateway
**Description:** As a maintainer, I want `test/smoke.sh` to run nightly against a real openclaw gateway so that wire-level regressions (auth, redaction, trace headers, models cache) are caught within 24 hours, not at the next release.

**Acceptance Criteria:**
- [ ] New workflow `.github/workflows/nightly.yml` triggered via `schedule: cron` (06:00 UTC) and `workflow_dispatch`.
- [ ] Repository secrets `CLAWCTL_HOST` and `CLAWCTL_GATEWAY_TOKEN` (or whatever `smoke.sh` reads) wired in.
- [ ] Job builds the Go binary fresh from `main`, then runs `smoke.sh` against it (not `clawctl.bash`) — `BIN=` env var override.
- [ ] On failure: workflow opens or comments on a tracking issue with the `nightly-smoke-failure` label (one issue per consecutive failure run, not one per night).
- [ ] Manual `workflow_dispatch` allows targeting a non-prod gateway via input parameter.

### US-006: Add Go-binary parity smoke for live gateway
**Description:** As a maintainer, I want a parity check that diffs `clawctl.bash` vs `clawctl` (Go) against the real gateway nightly, so the in-tree bash artifact stays a meaningful fixture for the parity scripts.

**Acceptance Criteria:**
- [ ] New script `test/smoke-parity.sh` that runs `health`, `models`, `raw GET /health`, and `verify` through both binaries against `$CLAWCTL_HOST` and diffs stdout (after canonicalizing trace ids, durations, and timestamps).
- [ ] Wired into the nightly workflow alongside `smoke.sh`.
- [ ] Differences are reported as `+got/-want` blocks with subcommand name, not just exit codes.

### US-007: Plugin manifest + directory convention test
**Description:** As a maintainer, I want automated tests that `.claude-plugin/plugin.json`, `.claude-plugin/marketplace.json`, and the `commands/` + `skills/` directory conventions stay valid so registry registration can't silently break.

**Acceptance Criteria:**
- [ ] New script `test/plugin-manifest.sh` validates `.claude-plugin/plugin.json` and `.claude-plugin/marketplace.json` both parse as JSON.
- [ ] `plugin.json` has the metadata keys currently present (`name`, `version`, `description`, `repository`, `license`); the test asserts these without inventing keys the manifest doesn't have.
- [ ] `version` matches a `vX.Y.Z` pattern.
- [ ] Every `commands/*.md` and `skills/*/SKILL.md` is non-empty and begins with either a frontmatter block or an H1 heading (whichever the existing files use — codify the current convention, don't change it).
- [ ] If `marketplace.json` references files or paths, those paths must exist.
- [ ] Wired into a new CI job `plugin` (Ubuntu).

### US-008: Plugin command contract test
**Description:** As a maintainer, I want each slash command's documented examples to be at least syntactically resolvable so that doc-vs-binary drift surfaces in CI.

**Acceptance Criteria:**
- [ ] Test extracts every fenced `clawctl …` invocation from `commands/clawctl.md`, `commands/clawctl-recipes.md`, `commands/clawctl-cli.md`.
- [ ] For each, runs the binary with `--help` on the named subcommand and asserts the subcommand exists (i.e. doesn't exit with usage error 2 for unknown command). Live-call testing is out of scope; this is a "command name still exists" check.
- [ ] Wired into the `plugin` CI job.
- [ ] Test name and failure messages name the source file + line so authors can find the broken example.

### US-009: Skill contract test for openclaw-loopback
**Description:** As a maintainer, I want `skills/openclaw-loopback/SKILL.md` to keep its documented invariants (R-1..R-12, label scheme, YAML deliverable header) checkable in CI.

**Acceptance Criteria:**
- [ ] New script `test/skill-loopback.sh` parses `SKILL.md` and asserts: all R-1..R-12 rule headings are present, the YAML deliverable header schema is valid YAML, and any referenced label names are listed in a single canonical block.
- [ ] Wired into the `plugin` CI job.
- [ ] Test fails with the specific missing rule / malformed block, not a generic "skill invalid".

### US-010: Linux keychain backend coverage in CI
**Description:** As a maintainer, I want `internal/keychain/keychain_linux*` tested on a real Linux runner so the secret-tool / pass / CLAWCTL_TOKEN_CMD fallback chain doesn't regress unnoticed.

**Acceptance Criteria:**
- [ ] CI matrix for the `go` job adds `linux` runner explicitly running `go test ./internal/keychain/...` with `-tags=linux` if needed.
- [ ] `secret-tool` and `pass` are installed in the runner via apt (or stubbed if installable cleanly is harder than stubbing — pick whichever the existing test file expects).
- [ ] Existing `keychain_linux_test.go` cases pass; if they require deps that aren't on the runner, install them or mark explicitly skipped with a logged reason.

### US-011: Release-tag end-to-end install + plugin smoke
**Description:** As a maintainer, when I push `v*`, I want CI to install the freshly-released binary via `install/install.sh` on macOS and Linux and exercise a representative plugin-driven invocation, so a release that breaks distribution is caught before users hit it.

**Acceptance Criteria:**
- [ ] Existing `install-smoke` job in `ci.yml` is **extended**, not replaced — it already gates on "has a release been published" and runs `version` + `--help`. Reuse that scaffolding.
- [ ] Job continues to run on `macos-latest` and `ubuntu-latest` (current matrix).
- [ ] Add steps after the existing version/help check: `clawctl health` against a mock local gateway (reuse the python `http.server` pattern from existing parity scripts) → `clawctl _redact` round-trip on a known leak fixture (verifies the redactor regex bundle survived the build) → `bash test/plugin-manifest.sh` (verifies the released binary's repo state still satisfies plugin contracts).
- [ ] On `release: published` events, failure additionally opens an issue with `release-broken` label (deduped — see US-005's pattern).
- [ ] Workflow uses the released artifacts (not a fresh `go build`) so it's truly testing the distribution path. The current job already does this; preserve it.
- [ ] Document in `test/README.md` (US-012) that this is the only path that exercises the *released* binary, not a freshly-built one.

### US-012: Document the test landscape
**Description:** As a contributor, I want a single page that lists what each test script covers, where it runs (PR / nightly / release), and what env it needs, so I can pick the right surface to extend.

**Acceptance Criteria:**
- [ ] New `test/README.md` (or a section in `CONTRIBUTING.md`) with a table: script name → what it tests → CI job → required env / deps → mocked-vs-live → bash-only / Go-capable.
- [ ] `CLAUDE.md` "Common commands" block updated to point at the new doc instead of listing scripts ad hoc.
- [ ] Doc explicitly names the boundary: "anything new that needs a live gateway goes in nightly, not PR".

### US-013: Make envelope + contract scripts target the Go binary
**Description:** As a maintainer, I want `envelope-*.sh`, `exit-codes.sh`, and `cli-hardening.sh` to support `BIN=` override so CI can validate the Go binary's contract conformance, not just `clawctl.bash`.

**Acceptance Criteria:**
- [ ] `test/envelope-msg.sh`, `test/envelope-stream.sh`, `test/envelope-redacted.sh`, `test/exit-codes.sh`, `test/cli-hardening.sh` all change `BIN="$ROOT/clawctl.bash"` to `BIN="${BIN:-$ROOT/clawctl.bash}"` (matching `smoke.sh`'s pattern).
- [ ] The `envelope` and `contract` CI jobs (US-002, US-003) run each script **twice**: once with the bash default, once with `BIN=$TMP/clawctl-go` after a `go build`. Both passes must succeed.
- [ ] Any test inside those scripts that depends on bash-specific behavior (e.g. exec format, error message wording from curl vs Go HTTP errors) is either generalized or guarded with an explicit `if [[ "$BIN" == *.bash ]]` block.
- [ ] No new wrapper script — modify in place.
- [ ] Existing `bash -n` and `shellcheck` checks continue to pass on the modified scripts.

### US-014: Gate release.yml on shell tests, not just go test
**Description:** As a maintainer, I want a `v*` tag push to fail before binaries are built if the parity / envelope / contract / plugin tests fail, so I can't ship a release whose mocked-gateway behavior is broken.

**Acceptance Criteria:**
- [ ] `release.yml`'s `build` job's `needs:` list extends to include the new `parity`, `envelope`, `contract`, and `plugin` jobs (or a single rollup job that depends on all of them).
- [ ] If a tagged build's mocked tests fail, `build` does not run and no GitHub Release is created.
- [ ] `actionlint` continues to pass on the updated workflow.
- [ ] Release path documented in `test/README.md` (US-012): "release.yml gates on the same mocked tests as ci.yml, plus the existing `go test` and actionlint."

### US-015: actionlint coverage for ci.yml and nightly.yml
**Description:** As a maintainer, I want every workflow lint-clean so a YAML typo can't silently break the test pipeline.

**Acceptance Criteria:**
- [ ] `actionlint` job (currently only in `release.yml`) is hoisted into `ci.yml`, scoped to all `.github/workflows/*.yml`. Same `raven-actions/actionlint@v2` action.
- [ ] `nightly.yml` (US-005) is covered by the same actionlint run.
- [ ] If keeping the existing actionlint job in `release.yml` is wasteful, remove it; otherwise leave it for defense-in-depth and note the duplication in `test/README.md`.

## Functional Requirements

- **FR-1:** CI must run `parity-health.sh`, `parity-models.sh`, `parity-raw.sh`, `parity-verify.sh`, `parity-redact.sh`, `parity-trace.sh` on every PR via a job named `parity`.
- **FR-2:** CI must run `envelope-msg.sh`, `envelope-stream.sh`, `envelope-redacted.sh`, `validate-fixtures.sh` on every PR via a job named `envelope`.
- **FR-3:** CI must run `exit-codes.sh`, `cli-hardening.sh`, `install-resolver.sh` on every PR via a job named `contract`.
- **FR-4:** The `go` CI job must produce a coverage profile and enforce a per-package floor on `internal/...` packages.
- **FR-5:** A nightly cron-scheduled workflow must run `smoke.sh` against the real gateway using repository secrets and surface failures via a tracking GitHub issue.
- **FR-6:** A `smoke-parity.sh` script must exist that diffs `clawctl.bash` and `clawctl` (Go) against the real gateway, and run nightly.
- **FR-7:** A `plugin` CI job must validate `.claude-plugin/plugin.json` schema and reference integrity, slash command examples' subcommand existence, and `skills/openclaw-loopback/SKILL.md` invariants.
- **FR-8:** The `go` job (or a sibling) must run `internal/keychain/...` tests on a Linux runner with secret-tool / pass available.
- **FR-9:** On `release: published`, the (extended) `install-smoke` job must install the released binary on `macos-latest` and `ubuntu-latest`, then exercise version, help, health (against local mock), redaction round-trip, and plugin manifest validation.
- **FR-10:** Each new shell test must pass `bash -n` and `shellcheck --severity=warning` (existing CI policy applies to new scripts too).
- **FR-11:** The test landscape document at `test/README.md` must be present and referenced from `CLAUDE.md`.
- **FR-12:** `envelope-*.sh`, `exit-codes.sh`, `cli-hardening.sh` must support `BIN=` override; CI jobs must run them once against `clawctl.bash` and once against the Go binary.
- **FR-13:** `release.yml`'s `build` job must depend on the new mocked-test jobs (`parity`, `envelope`, `contract`, `plugin`) so a broken-mocked-tests tag can't produce binaries.
- **FR-14:** `actionlint` must run in `ci.yml` (not only in `release.yml`) and cover all `.github/workflows/*.yml` including the new `nightly.yml`.

## Non-Goals

- No mutation testing, no property-based testing beyond the existing SSE fuzz target.
- No live-call testing of slash commands' examples (only "subcommand exists" checks).
- No Windows runners — clawctl is macOS + Linux today (`internal/keychain` is darwin/linux only).
- No load/perf testing of the gateway path.
- No expansion of fuzz targets beyond `FuzzParseSSE` (out-of-scope; can be a follow-up).
- No cross-version compatibility matrix for openclaw gateway versions.
- No replacement of `clawctl.bash` parity testing with anything else — bash leftover is still load-bearing for parity until it's deleted in a separate PRD.
- No CI for `ralph.sh` (separate concern, called out in CLAUDE.md).

## Technical Considerations

- **Mock gateways already exist.** Most parity scripts spin up a Python `http.server` on `127.0.0.1:<random>`; envelope scripts do the same. Reuse this pattern in US-011 instead of inventing a new mock.
- **Bash + Go dual targets.** Parity scripts already build the Go binary into a temp path (`GO_BIN`); the envelope/exit-codes/cli-hardening scripts do **not** — they hardcode `clawctl.bash`. US-013 changes that. Until US-013 lands, the `envelope` and `contract` jobs only validate the bash leftover.
- **`security` shadowing on Linux.** Existing scripts shadow macOS `security` on `PATH` to inject a fake bearer. On Ubuntu runners there is no `security` to shadow, but the stub is added to `PATH` directly so this works regardless of host OS — confirmed by reading `parity-health.sh` and friends. Still, validate when wiring CI: a missing `security` resolution path could throw a different error on Linux.
- **`ajv-cli` install cost.** Envelope scripts use `npx ajv-cli`; cache `~/.npm` in the workflow or install ajv-cli once at job start to avoid 30s+ cold installs per run.
- **Coverage floor pinned.** US-004 fixes the floor at **65%** based on the 2026-05-10 baseline. Don't re-measure during implementation; just enforce.
- **Nightly issue spam.** US-005 must dedupe failure issues — open one issue per "streak of failed runs", append a comment to the existing open issue on subsequent failures, close+comment when nightly returns to green.
- **Secrets handling.** `CLAWCTL_GATEWAY_TOKEN` for nightly should be a dedicated, low-privilege gateway token — not the maintainer's personal token. Document this in `test/README.md`.
- **Stderr discipline holds in tests too.** Test scripts must not pipe stderr into stdout assertions; stick to the documented split (trace ids → stderr, content → stdout).
- **`smoke.sh` already supports `BIN=` override.** US-005 just needs to set `BIN=$(go build -o … ./cmd/clawctl)` before invoking it.
- **Plugin manifest is metadata-only.** `.claude-plugin/plugin.json` does not list `commands` or `skills` paths — discovery is by directory convention. US-007 must validate the convention, not a manifest schema that doesn't exist.
- **`release.yml` currently runs `go test` but no shell tests** before building binaries. US-014 fixes this. Note that the existing `release.yml` `test` job and `ci.yml` `go` job duplicate work — that's tolerable; the value is gating the release path on the same checks as the merge path.
- **`actionlint` already runs in `release.yml`.** US-015 hoists it to `ci.yml`. If kept in both, the duplication is acceptable; if removed from one, document it.

## Success Metrics

- Every PR run executes ≥ 13 distinct test scripts (currently: 0 invoked beyond `bash -n` and `shellcheck`).
- Go `internal/...` coverage is measured on every PR and the floor is enforced.
- Nightly smoke run history shows green for 7+ consecutive days after rollout (pre-existing flakes get fixed, not muted).
- A regression in `clawctl msg --envelope` schema is caught by CI within one PR cycle (manually verified by intentionally breaking the envelope and confirming the `envelope` job fails).
- A regression in install.sh resolution (e.g. wrong artifact name for an OS/arch) is caught on the next release tag, not by a user issue.
- The `test/README.md` table exists and lists every script in `test/` (no orphans).

## Open Questions

- ~~What's the right coverage floor for `internal/`?~~ — **Resolved**: 65% per package, baseline 2026-05-10.
- Should the nightly run also test against an older released binary (regression fence for the gateway)? — leaning no; out of scope unless gateway team asks for it.
- Where does the dedicated nightly gateway token live? — needs a maintainer decision before US-005 can ship.
- ~~Does the plugin manifest schema have an upstream definition we can validate against?~~ — **Resolved**: no — `plugin.json` is metadata-only, discovery is by directory convention. US-007 validates conventions.
- US-013 ordering: do envelope/contract scripts get the `BIN=` override **before** or **after** they're wired into CI? — recommend: PR 1 wires them in for bash (US-002, US-003); PR 2 adds `BIN=` override and the dual-pass run (US-013). Splits the risk.
- Is there appetite for moving `clawctl.bash` out of the repo entirely once parity is locked in CI? — out of scope here, but the parity jobs are a prerequisite for that future cleanup.
- Should US-014 land before or after the next `v*` tag? — recommend after, so we don't hold up an in-flight release on test-pipeline scope.
