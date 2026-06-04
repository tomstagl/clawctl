# Design principles

These principles determine what gets in and what stays out. They're more important than any single feature.

## 1. Read-only by default

Every command that mutates state — adding agents, scheduling crons, installing skills, rotating tokens — must be:

- Behind an explicit `clawctl cli <subcommand>` path.
- Documented as mutating in the help text and recipes.
- Confirmed by the user before invocation by any consumer (the Claude plugin enforces this; humans should too).

The wrapper itself never has a `--force` shortcut, an alias for a destructive operation, or a "convenience" flag that turns one into the other.

## 2. No secrets on disk

Bearer tokens live in the platform credential store. The binary reads them at call time and never persists them anywhere — not env, not config files, not cache.

- **macOS**: Keychain via `security find-generic-password` (`internal/keychain/keychain_darwin.go`).
- **Linux**: `secret-tool` or `pass`, in that order (`internal/keychain/keychain_linux.go`) — both implemented today, not aspirational.
- **Any platform**: `CLAWCTL_TOKEN_CMD` is honoured first as an explicit override, running an arbitrary command whose stdout is the token.

Plain-text fallbacks and reading a `CLAWCTL_TOKEN` env var are not acceptable. Windows (`cmdkey`) remains a future port.

## 3. Trace every call

Every outbound HTTP call attaches a W3C `traceparent`. The trace-id is printed to stderr so users can cite it instead of dumping bodies. Reporting an issue means quoting `trace-id: <32-hex>`, not pasting JSON.

`clawctl trace` is the inverse — given a trace-id, print the Jaeger UI link and the first 30 spans.

## 4. Redact at the boundary

Even with strict prompts upstream, agents leak. The wrapper masks known patterns (`dt0c01.*`, `dt0s16.*`, `gh[psoru]_*`, AWS access keys, JWTs, gateway-token literal) before output reaches the terminal.

A hit:
- Replaces the value with `<REDACTED:kind:first-11-chars…>`.
- Prints a stderr warning naming the kind and source agent.
- Appends to `~/.cache/clawctl/last-redaction` (append-only audit).

`CLAWCTL_NO_REDACT=1` is for one-shot debugging. It's never the default and CI must never set it.

## 5. One binary, zero runtime deps

`clawctl` is a single static Go binary (`CGO_ENABLED=0`), built from `cmd/clawctl/`. It carries its own transport, auth, redaction, SSE parsing, and MCP server — nothing is shelled out that can be done in-process.

Runtime dependencies are limited to OS integrations that have no portable Go equivalent, and are reached by shelling out rather than linking CGO:

- `ssh` — only for `clawctl cli` (host-side `openclaw` ops).
- `security` (macOS) / `secret-tool` or `pass` (Linux) — only for reading the bearer token (principle #2).
- Optional: `git` and `gh` for `clawctl verify`.

The deprecated `clawctl.bash` original is kept only for parity testing (`test/parity-*.sh`); don't add features to it. If a feature needs a heavier runtime (npm, a Python venv), it lives in a separate repo.
