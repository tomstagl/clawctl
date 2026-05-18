# Test Landscape

This document describes every test script in `test/`, where it runs, what it requires, and whether it works against the mock infrastructure only or a live gateway.

**Rule:** anything that needs a live gateway goes in **nightly**, not in PR CI. PR jobs must complete without network access to the real openclaw gateway.

## Script table

| Script | What it tests | CI job | Required env / deps | Mocked vs live | bash / Go |
|---|---|---|---|---|---|
| `parity-health.sh` | `clawctl health` bash↔Go output parity (200, 500, unreachable, no-host) | `parity` | python3, jq, Go toolchain | Mocked (python3 HTTP server) | Both |
| `parity-models.sh` | `clawctl models` bash↔Go parity incl. cache-hit detection | `parity` | python3, jq, Go toolchain, macOS `security` (skips on Linux) | Mocked (python3 HTTP server) | Both |
| `parity-raw.sh` | `clawctl raw` GET/POST bash↔Go parity; traceparent forwarding | `parity` | python3, jq, Go toolchain, macOS `security` (skips on Linux) | Mocked (python3 HTTP server) | Both |
| `parity-verify.sh` | `clawctl verify` (commit/pr/issue/file/help/error) bash↔Go parity; fake `gh` shim | `parity` | Go toolchain, git | Mocked (fake `gh` binary + fixture git repo) | Both |
| `parity-redact.sh` | `clawctl _redact` — 20 fixture cases covering every documented secret pattern | `parity` | Go toolchain | Mocked (no network) | Both |
| `parity-trace.sh` | `clawctl trace` bash↔Go parity against fixture Jaeger responses | `parity` | python3, jq, Go toolchain | Mocked (python3 HTTP server) | Both |
| `envelope-msg.sh` | `clawctl msg` v1 ToolResponse JSON schema validation; plain-text fallback | `envelope` | python3, jq, curl, npx (ajv-cli@5) | Mocked (python3 HTTP server) | Both (bash default, `BIN=` for Go) |
| `envelope-stream.sh` | `clawctl stream` NDJSON chunk + terminal ToolResponse schema validation | `envelope` | python3, jq, curl, npx (ajv-cli@5) | Mocked (python3 HTTP server) | Both |
| `envelope-redacted.sh` | Redaction events in `envelope.redactions[]`; stderr WARNING; audit file | `envelope` | python3, jq, curl, npx (ajv-cli@5) | Mocked (python3 HTTP server) | Both |
| `validate-fixtures.sh` | Static envelope fixture files in `test/fixtures/envelope/` against v1 schema | `envelope` | npx (ajv-cli@5) | Mocked (no network) | Bash only |
| `exit-codes.sh` | Exit-code contract (0/2/6/7/22/28) for every subcommand × failure mode | `contract` | (curl/security stubs via PATH) | Mocked (stub binaries) | Both |
| `cli-hardening.sh` | `clawctl cli` SSH argv forwarding; oc-remote absent/present; metachar safety | `contract` | (ssh stub via PATH) | Mocked (stub binaries) | Both |
| `install-resolver.sh` | `install/install.sh` end-to-end: platform detection, checksum, upgrade/refuse | `contract` | (curl/uname stubs via PATH) | Mocked (fixture tree) | Bash only (tests the installer) |
| `plugin-manifest.sh` | `.claude-plugin/plugin.json` + `marketplace.json` validity; file-header convention | `plugin`, `install-smoke` | jq | Mocked (no network) | Bash only |
| `plugin-commands.sh` | Subcommand name-existence: every `clawctl <sub>` in command docs resolves in Go binary | `plugin` | Go toolchain | Mocked (no network) | Go only |
| `skill-loopback.sh` | `skills/openclaw-loopback/SKILL.md`: R-1..R-12 headings; YAML header parses; label locality | `plugin` | python3-yaml (optional, graceful fallback) | Mocked (no network) | Bash only |
| `smoke.sh` | End-to-end health, models, traceparent, redact, verify against real gateway | **nightly** | `CLAWCTL_HOST`, `CLAWCTL_GATEWAY_TOKEN`, macOS Keychain | **Live gateway** | Both (`BIN=` override) |
| `smoke-parity.sh` | bash↔Go output diff for health/models/raw/verify against real gateway | **nightly** | `CLAWCTL_HOST`, macOS `security` (skips auth-gated on Linux) | **Live gateway** | Both |

## CI job summary

| Job | Trigger | Scripts run |
|---|---|---|
| `parity` | PR + main push | parity-health, parity-models, parity-raw, parity-verify, parity-redact, parity-trace |
| `envelope` | PR + main push | validate-fixtures, envelope-msg (×2), envelope-stream (×2), envelope-redacted (×2) |
| `contract` | PR + main push | exit-codes (×2), cli-hardening (×2), install-resolver |
| `plugin` | PR + main push | plugin-manifest, plugin-commands, skill-loopback |
| `install-smoke` | PR + main push (skips if no release) | install.sh, version check, health mock, _redact round-trip, plugin-manifest |
| `go` | PR + main push | go vet, go test (coverage ≥65% for internal/), fuzz SSE |
| **nightly** | Daily 06:00 UTC + manual dispatch | smoke.sh, smoke-parity.sh |
| **mocked-tests** (release.yml) | `v*` tag push | All parity, envelope, contract, plugin scripts (gates the build) |

Scripts marked ×2 run once with the bash binary and once with the freshly-built Go binary.

## Nightly secrets

The nightly workflow (`nightly.yml`) requires two repository secrets:

| Secret | Purpose |
|---|---|
| `CLAWCTL_HOST` | URL of the openclaw gateway to test against (e.g. `https://gateway.example.com`) |
| `CLAWCTL_GATEWAY_TOKEN` | Bearer token for the gateway; injected via `CLAWCTL_TOKEN_CMD="echo $CLAWCTL_GATEWAY_TOKEN"` |

**Use a dedicated low-privilege gateway token** for nightly testing — scoped to the minimum permissions needed for health, models, and raw GET calls. Do not reuse a production credential or a token with write access.

On nightly failure a GitHub issue is automatically opened with the label `nightly-smoke-failure`. On recovery the issue is closed with a "recovered" comment.
