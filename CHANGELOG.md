# Changelog

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
