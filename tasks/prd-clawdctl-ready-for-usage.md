# PRD: Make clawctl Ready for Usage

## Introduction

`clawctl` is the local-side client that lets a developer's local LLM (Claude Code, Codex, or any other agent host) talk to an `openclaw` gateway and dispatch work to an agent fleet running there. The first version is a single-file bash script (493 lines) covering health, model listing, chat (`msg`/`stream`), arbitrary auth'd HTTP (`raw`), SSH-tunneled `openclaw` CLI invocations, R-2 claim verification, and Jaeger trace lookup.

The script works, but it was not yet evaluated for *inter-LLM communication quality*: do its transport choices (HTTP API vs SSH vs direct CLI), its streaming envelope, its error model, and its tool surface let a local LLM call openclaw agents reliably as typed tools? This PRD plans the work to (a) review and refactor the bash MVP's transport choices in place, then (b) rewrite the client as a typed binary that exposes an MCP server frontend and a JSON-schema'd tool envelope, so Claude Code, Codex, and other local LLMs can register clawctl as a first-class tool.

The work is structured as two phases inside one PR (Phase 1 lands as commits; Phase 2 lands as commits behind it). An implementation-time roster of expert sub-agent personas (Task subagents) will own discrete workstreams.

---

## Goals

- Document a canonical **transport decision matrix** (HTTP API vs SSH vs local CLI) and refactor any subcommand that picks the wrong transport.
- Harden the SSH path: `ControlMaster` reuse, `clawctl-remote` becomes required (not an optional fallback), stricter argv handling, no shell-string interpolation on the host.
- Define a versioned **JSON-schema tool envelope** (request, response, streaming chunk, error) so a local LLM can call any openclaw agent as a typed function/tool.
- Ship a **typed binary** (Go) replacing the bash entrypoint, with a first-class API client, structured streaming, and the same surface area the bash script has today.
- Ship an **MCP server mode** in the typed binary so Claude Code (and any MCP-capable client) can register clawctl as an MCP server and call openclaw agents through stdio MCP.
- Stand up an **implementation-time agent team** (sub-agent personas, see §10) that owns the phased workstreams in parallel.
- Preserve current safety properties: client-side response redaction, R-2 claim verification, W3C traceparent propagation, macOS-keychain-stored bearer token.

---

## User Stories

### US-001: Transport decision matrix (doc artifact)
**Description:** As a contributor, I want a written matrix of which transport (HTTP API, SSH, local CLI) is correct for which operation, so future subcommands don't get implemented over the wrong transport.

**Acceptance Criteria:**
- [ ] `docs/transport-decisions.md` lists every current subcommand with: operation, chosen transport, rationale, alternative considered, exit-code contract
- [ ] Matrix covers at least: `health`, `models`, `msg`, `stream`, `raw`, `cli`, `verify`, `trace`
- [ ] Each row cites the failure mode the chosen transport avoids (e.g. SSH for `cli` because gateway lacks an exec-arbitrary-shell endpoint by design)
- [ ] Reviewed by openclaw-gateway-expert and security-redaction-expert sub-agents

### US-002: Harden the SSH path
**Description:** As an operator, I want SSH-based subcommands to reuse a control connection, fail closed when `clawctl-remote` is missing, and never construct shell strings, so SSH usage is fast, predictable, and not an injection surface.

**Acceptance Criteria:**
- [ ] `~/.ssh/config` snippet (in `install/`) sets `ControlMaster auto`, `ControlPath ~/.ssh/cm-%r@%h:%p`, `ControlPersist 10m` for `CLAWCTL_SSH_HOST`
- [ ] `clawctl cli` exits 2 with a clear remediation message if `/usr/local/bin/clawctl-remote` is not present on the host (no fallback shell-quoting path)
- [ ] All SSH invocations pass argv via `--` to `clawctl-remote`; no `printf %q`-into-string code path remains
- [ ] Connection-reuse measurable: `clawctl cli openclaw status` second call ≤ 200 ms over a warm `ControlMaster`
- [ ] `test/` includes a smoke test (mock SSH server or skip-if-no-host) covering the hardened path
- [ ] Typecheck/shellcheck passes

### US-003: JSON-schema tool envelope (versioned)
**Description:** As a local LLM, I need a typed envelope for request, response, streaming chunks, and errors so I can register openclaw agents as tools/functions and reason about their outputs structurally.

**Acceptance Criteria:**
- [ ] `schemas/envelope.v1.json` defines: `ToolRequest`, `ToolResponse`, `ToolStreamChunk`, `ToolError`, with `envelope_version: "1"` discriminator
- [ ] Envelope carries: `agent`, `session_id?`, `traceparent`, `input`, `tool_choice?`, `redactions[]`, `usage`, `finish_reason`
- [ ] Error model maps gateway HTTP errors to typed `ToolError.code` values (e.g. `transport.connection_refused`, `gateway.rate_limited`, `agent.unknown`, `redaction.applied`)
- [ ] JSON Schema validates against fixtures in `test/fixtures/envelope/*.json` (happy path, streaming, error, redacted)
- [ ] Envelope versioning policy documented in `docs/envelope.md` (additive changes only within v1; breaking changes bump to v2)
- [ ] Reviewed by claude-mcp-expert and openclaw-gateway-expert sub-agents

### US-004: Bash entrypoint emits the v1 envelope
**Description:** As a user of the bash MVP today, I want `--envelope` mode on `msg`/`stream`/`raw` so I can already get typed JSON output before the typed binary lands.

**Acceptance Criteria:**
- [ ] `clawctl msg --envelope AGENT TEXT` emits a `ToolResponse` JSON document on stdout
- [ ] `clawctl stream --envelope AGENT TEXT` emits one `ToolStreamChunk` per line (NDJSON), terminated by a final `ToolResponse`
- [ ] Redaction hits are reported in the envelope (`redactions[]`) instead of only stderr, when `--envelope` is set
- [ ] Exit codes for envelope mode are still those documented in `clawctl help`
- [ ] Tests in `test/` validate envelope output against `schemas/envelope.v1.json` (happy, error, redacted, streaming)
- [ ] Shellcheck passes

### US-005: Structured exit codes audit
**Description:** As a local LLM caller, I need every subcommand's exit codes to follow the documented contract so I can branch on them deterministically.

**Acceptance Criteria:**
- [ ] Every `case "$cmd"` branch in `clawctl` exits with a code from the table in `clawctl help`
- [ ] Any deviation is either fixed or added to the help table with rationale
- [ ] `test/exit-codes.sh` table-tests each subcommand × failure mode pair
- [ ] Shellcheck passes

### US-006: Language pick for the typed binary (decision doc)
**Description:** As the team, we need to pick Go or Rust for the typed binary based on concrete criteria, not vibes, so the rewrite has a defensible rationale.

**Acceptance Criteria:**
- [ ] `docs/typed-binary-language.md` compares Go vs Rust on: MCP SDK maturity, single-file static binary size, cross-compile to darwin/linux, HTTP+SSE client ergonomics, ssh client library quality, team familiarity
- [ ] Decision recorded with the criterion that broke the tie
- [ ] Default recommendation: **Go** (mature MCP SDK, mature `golang.org/x/crypto/ssh`, `net/http` SSE handling, single static binary, `~10 MB`); document reasons to override if Rust is chosen instead
- [ ] Reviewed by claude-mcp-expert sub-agent

### US-007: Typed binary scaffold + API client
**Description:** As a developer, I want a minimal `clawctl` typed binary that compiles, has the same env-var surface, and implements `health`, `models`, and `raw` parity with the bash version.

**Acceptance Criteria:**
- [ ] New `cmd/clawctl/` (Go) compiles to a single static binary under 15 MB
- [ ] `clawctl health` and `clawctl models` produce byte-identical JSON to bash version against the same gateway
- [ ] `clawctl raw METHOD PATH ...` parity, including traceparent header generation
- [ ] Bearer token loaded from macOS keychain via `security` shellout (or `github.com/keybase/go-keychain`)
- [ ] Unit tests for traceparent generation, env-var resolution, keychain loader (interface mocked)
- [ ] `go vet ./... && go test ./...` passes; CI workflow added

### US-008: Typed binary parity for `msg`/`stream` with envelope
**Description:** As a developer, I want the typed binary's chat path to default-emit the v1 envelope and stream chunks losslessly.

**Acceptance Criteria:**
- [ ] `clawctl msg AGENT TEXT` emits `ToolResponse` JSON by default in the typed binary
- [ ] `clawctl stream AGENT TEXT` emits NDJSON `ToolStreamChunk`s ending with `ToolResponse`
- [ ] `--text` flag falls back to plain-text output (parity with current bash UX)
- [ ] SSE chunk parser is fuzz-tested (boundary-split, partial UTF-8, missing `[DONE]`)
- [ ] Redaction layer ported with parity test against the bash perl regex set
- [ ] `go test ./...` passes including fuzz seeds

### US-009: MCP server mode
**Description:** As a Claude Code user, I want to run `clawctl mcp` and register the binary as an MCP server in my settings so I can call any openclaw agent as a tool from inside Claude.

**Acceptance Criteria:**
- [ ] `clawctl mcp` runs an MCP stdio server using the official Go MCP SDK
- [ ] Each agent returned by `/v1/models` is exposed as one MCP tool with a JSON-schema'd input matching the v1 envelope `input` shape
- [ ] `tools/list` enumerates agents; `tools/call` executes via the gateway and streams `ToolStreamChunk`s as MCP progress notifications, then returns the final `ToolResponse`
- [ ] Trace propagation: MCP request id → traceparent on the gateway call → returned to the client
- [ ] `docs/mcp.md` documents the `claude mcp add` command needed to register clawctl
- [ ] End-to-end test: spawn `clawctl mcp` in a subprocess, call `tools/list` and `tools/call` via MCP client, assert envelope shape

### US-010: Hardened SSH path in the typed binary
**Description:** As an operator, I want the typed binary's `cli` subcommand to use a long-lived ssh client connection (or `ControlMaster`-aware spawn) and refuse to run if `clawctl-remote` is missing.

**Acceptance Criteria:**
- [ ] Typed `clawctl cli ...` uses `golang.org/x/crypto/ssh` to dial the host once per process and reuse the connection for multiple invocations within that process; or shells out to `ssh` with `ControlMaster`-aware flags
- [ ] Fails closed (exit 2 with remediation) when `clawctl-remote` is absent
- [ ] argv passed as a string slice; never concatenated into a shell command
- [ ] Integration test against a local mock ssh server (e.g. `gliderlabs/ssh`) validates argv preservation including spaces, quotes, `$`, backticks
- [ ] `go test ./...` passes

### US-011: Observability parity (traces + structured logs)
**Description:** As an operator debugging a failed call, I want every subcommand in the typed binary to log a structured event with traceparent, agent, transport, latency, exit code.

**Acceptance Criteria:**
- [ ] Structured JSON logs to stderr behind `CLAWCTL_LOG=json` (default: human-friendly)
- [ ] Each line includes: `ts`, `traceparent`, `agent?`, `transport` (`api|ssh|local`), `subcommand`, `latency_ms`, `exit_code`, `redactions_count`
- [ ] `clawctl trace TRACE-ID` works against typed binary (parity with bash)
- [ ] Redaction is applied to log lines using the same patterns as response redaction

### US-012: Distribution + install
**Description:** As a new user, I want a single command to install the typed binary on macOS and Linux.

**Acceptance Criteria:**
- [ ] `install/install.sh` downloads the right release binary for `uname -s/-m` and writes it to `/usr/local/bin/clawctl`
- [ ] GitHub Actions release workflow builds darwin-arm64, darwin-amd64, linux-amd64, linux-arm64
- [ ] `clawctl version` prints semver + commit hash
- [ ] README quickstart updated with both `brew tap`-style instructions (deferred) and `install.sh` curl one-liner
- [ ] Old bash `clawctl` kept in repo as `clawctl.bash` for one release cycle, with deprecation notice in `--help`

### US-013: Standing up the implementation agent team
**Description:** As the PR driver, I want to spawn an implementation-time roster of expert sub-agents (via Task) so the workstreams above run in parallel with deep expertise per area.

**Acceptance Criteria:**
- [ ] `docs/agent-team.md` lists each persona with: name, charter, owned user stories, success criterion
- [ ] At least these personas exist (see §10 for full charters):
  - openclaw-gateway-expert
  - claude-mcp-expert
  - security-redaction-expert
  - bash-cli-ergonomics-expert
  - go-binary-lead
  - observability-expert
- [ ] Each persona's owned stories explicitly named; no story is unowned; no story has more than one primary owner
- [ ] Personas are spawned via the `Task` tool with `subagent_type: general-purpose` and a charter prompt that briefs them on the transport decision matrix and the v1 envelope

---

## Functional Requirements

### Phase 1 — Bash MVP review and refactor

- FR-1: Produce `docs/transport-decisions.md` covering every existing subcommand. (US-001)
- FR-2: Add `~/.ssh/config` snippet under `install/` enabling `ControlMaster auto` for `CLAWCTL_SSH_HOST`. (US-002)
- FR-3: Make `clawctl-remote` on the host a hard requirement for `clawctl cli`; remove the `printf %q` shell-string fallback. (US-002)
- FR-4: Define `schemas/envelope.v1.json` with `ToolRequest`, `ToolResponse`, `ToolStreamChunk`, `ToolError`, `envelope_version`. (US-003)
- FR-5: Add `--envelope` flag to `msg`, `stream`, and `raw` in the bash entrypoint emitting the v1 envelope. (US-004)
- FR-6: Move redaction-hit reporting into the envelope under `redactions[]` when `--envelope` is set, in addition to the existing stderr warning. (US-004)
- FR-7: Audit and fix every subcommand to honor the exit-code table in `clawctl help`. (US-005)
- FR-8: Pick Go or Rust for the typed binary; record the decision in `docs/typed-binary-language.md`. (US-006)

### Phase 2 — Typed binary + MCP server

- FR-9: Implement `cmd/clawctl/` in Go with `health`, `models`, `raw`, `msg`, `stream`, `cli`, `verify`, `trace` parity. (US-007, US-008, US-010)
- FR-10: Default chat output in the typed binary is the v1 envelope; `--text` falls back to plain text. (US-008)
- FR-11: Implement `clawctl mcp` MCP stdio server exposing each `/v1/models` agent as a typed tool. (US-009)
- FR-12: SSH usage in the typed binary uses `ControlMaster` (or in-process connection reuse) and passes argv as a slice — never as a shell string. (US-010)
- FR-13: Structured JSON logs behind `CLAWCTL_LOG=json` with traceparent, transport, latency, exit code. (US-011)
- FR-14: Releases produce darwin-arm64, darwin-amd64, linux-amd64, linux-arm64 binaries; `install/install.sh` resolves the right one. (US-012)
- FR-15: Spawn the agent team described in §10 via `Task` with explicit story ownership before implementation begins. (US-013)

---

## Non-Goals (Out of Scope)

- **Not** changing the gateway side (`openclaw`). All changes are local-client only. If a gateway change is required (e.g. new endpoint to avoid an SSH path), it is captured as an Open Question, not implemented here.
- **Not** adding new subcommands beyond what bash has today (no `fleet`, no `spawn`, no `agent` CRUD). The scope is parity + envelope + MCP, not surface expansion.
- **Not** building a Windows binary in this PR. macOS + Linux only.
- **Not** replacing the macOS-keychain bearer-token model with mTLS, OAuth, or SSH-tunneled tokens. Recorded as an Open Question.
- **Not** generalizing the redaction pattern set. The current pattern list is the spec; expansion is a separate PR.
- **Not** building a homebrew formula or apt package in this PR. Curl-install + raw GitHub release downloads only.
- **Not** removing the bash `clawctl` immediately. It stays as `clawctl.bash` for one release cycle.
- **Not** writing tests for the gateway itself, only for the client behavior against fixtures and a mock gateway.

---

## Design Considerations

- **Single binary:** the typed `clawctl` must remain a single static executable. No runtime-loaded plugins, no shared libraries beyond libc. This keeps the install story `curl | sh`-simple.
- **MCP transport:** stdio MCP only in v1 (matches Claude Code's local registration model). HTTP/SSE MCP can be added later without breaking changes.
- **Envelope versioning:** additive changes within `v1`; breaking changes bump to `v2`. Both versions can be supported by the binary in parallel for one release cycle.
- **Redaction layering:** redaction lives in the client and is applied *before* the envelope is emitted. Redacted spans are still surfaced via `redactions[]` so the LLM caller knows a redaction happened without seeing the secret.
- **Reuse:** the `_redact` perl block, the SSE parser, and the `_explain_http_error` jq logic are the spec for the Go ports. Parity tests must pin behavior against the bash output.
- **Help/UX:** keep the kubectl-style verb-noun ergonomics. Don't introduce a global flag parser that breaks `clawctl msg -s SESSION agent text` ordering.

---

## Technical Considerations

- **Go MCP SDK:** use `github.com/modelcontextprotocol/go-sdk` (official). Confirm version pin and stdio-server example before US-009.
- **Go SSH client:** prefer `golang.org/x/crypto/ssh` for in-process dialing; alternative is shelling out to `ssh` with `-o ControlMaster=auto` so the user's `~/.ssh/config` and agent forwarding still apply. Decide in US-010.
- **Keychain access:** prefer `os/exec` shellout to `security` to avoid CGo (`go-keychain` requires CGo and complicates cross-compilation). Document the choice.
- **SSE parsing:** parser must be byte-stream-aware, not line-aware-then-decode, to handle UTF-8 multibyte boundaries. Add fuzz seeds at byte offsets that split codepoints.
- **Trace propagation:** generate W3C traceparent in the binary; propagate it on every gateway call AND every MCP tool result (in `_meta`).
- **Schema validation:** ship the JSON Schema in the binary (`go:embed`) so `clawctl validate-envelope` can self-check.
- **Concurrency:** MCP tool calls are independent; the server should handle them concurrently. Each call gets its own traceparent and SSH/HTTP client (or shared connection-pooled client).
- **Error budget:** surface gateway error codes via typed `ToolError.code` strings — do not leak HTTP status codes as the only signal.

---

## Success Metrics

- Local LLM (Claude Code) can register clawctl as an MCP server in one step and successfully invoke at least three openclaw agents as tools, end-to-end, with traceparent propagated to Jaeger.
- Streaming throughput from the typed binary matches or exceeds the bash version (target: zero chunk loss at 100 chunks/sec).
- `clawctl cli` warm-call latency ≤ 200 ms over a warm `ControlMaster`.
- Every documented exit code matches actual binary behavior in CI table tests.
- Envelope JSON validates against `schemas/envelope.v1.json` for 100% of fixture cases.
- Single-binary install (`curl install.sh | sh`) succeeds on a clean macOS-arm64 and ubuntu-22.04-amd64 host.
- Bash `clawctl` and typed `clawctl` produce byte-identical output for `health`, `models`, `verify` for at least one release cycle.

---

## §10 — Implementation Agent Team Roster

Spawned via the `Task` tool with `subagent_type: general-purpose`. Each charter prompt includes: this PRD, the transport decision matrix (US-001), and the v1 envelope schema (US-003). Personas operate in parallel where their owned stories don't overlap.

### openclaw-gateway-expert
- **Charter:** Owns the gateway-facing contract. Knows every `/v1/*` endpoint clawctl calls today, knows the redaction pattern set, knows what the gateway *will not* expose (rationale for SSH `cli`).
- **Owns:** US-001 (review), US-003 (envelope error-code mapping), US-004 (envelope mode in bash), US-007 (API-client parity)
- **Success:** transport matrix and envelope reflect actual gateway behavior, not assumed behavior.

### claude-mcp-expert
- **Charter:** Owns the MCP integration. Knows the Go MCP SDK, knows Claude Code's MCP registration UX, knows which capabilities the binary must declare for streaming + progress notifications.
- **Owns:** US-006 (language pick — MCP SDK maturity is the dominant criterion), US-009 (MCP server mode)
- **Success:** Claude Code can `claude mcp add clawctl` in one command and call openclaw agents as tools.

### security-redaction-expert
- **Charter:** Owns the redaction layer and the auth model. Reviews every code path that touches the bearer token, the SSH argv handling, and the redaction patterns.
- **Owns:** US-002 (SSH hardening — argv handling), US-004 (redaction reporting in envelope), redaction Go port in US-008
- **Success:** no shell-string interpolation remains; redaction parity is byte-tested against the bash version.

### bash-cli-ergonomics-expert
- **Charter:** Owns the bash MVP polish work. Keeps the bash entrypoint kubectl-flavored and shellcheck-clean while we transition.
- **Owns:** US-002 (install/ssh-config snippet), US-004 (bash `--envelope` flag), US-005 (exit-code audit)
- **Success:** bash `clawctl` ships with envelope mode and is fully shellcheck-clean.

### go-binary-lead
- **Charter:** Owns the typed binary scaffold, build, and release pipeline.
- **Owns:** US-006 (decision doc), US-007 (scaffold + parity), US-008 (chat + envelope), US-010 (SSH path), US-012 (distribution)
- **Success:** typed binary reaches parity and ships releases.

### observability-expert
- **Charter:** Owns trace propagation, structured logs, and the `clawctl trace` subcommand.
- **Owns:** US-011 (structured logs + traces in typed binary), traceparent propagation in US-009 (MCP)
- **Success:** every call from the local LLM through to a Jaeger span is correlatable end-to-end.

**Coordination:** the PR driver (you) reviews each persona's PR-internal commit before it lands. A persona that hits a cross-cutting concern (e.g. envelope shape changes mid-implementation) opens a discussion via the PR description — not a unilateral edit to another persona's owned stories.

---

## Open Questions

- **OQ-1:** Should the gateway expose a structured "agent describe" endpoint so MCP `tools/list` can surface real input schemas per agent, instead of a generic envelope shape? (Affects US-009.)
- **OQ-2:** Auth: keep macOS-keychain bearer token, or move to short-lived tokens issued by an SSO flow? Out of scope for this PR but the typed binary should leave room.
- **OQ-3:** Should `clawctl mcp` also be reachable over HTTP/SSE MCP (not just stdio) for non-Claude clients in CI? Likely v2.
- **OQ-4:** Do we want a `clawctl agents` or `clawctl fleet` subcommand for listing+inspecting fleet agents (richer than `models`)? Suggested next-PR scope.
- **OQ-5:** Linux distribution: is curl-install enough for v1, or do we need a homebrew tap on day one for adoption?
- **OQ-6:** Should the typed binary embed a `clawctl doctor` command that runs the install/preflight checks (token present, host reachable, clawctl-remote installed, ControlMaster configured)? Likely a small follow-up.
