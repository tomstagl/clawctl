# Design principles

These principles determine what gets in and what stays out. They're more important than any single feature.

## 1. Read-only by default

Every command that mutates state — adding agents, scheduling crons, installing skills, rotating tokens — must be:

- Behind an explicit `clawctl cli <subcommand>` path.
- Documented as mutating in the help text and recipes.
- Confirmed by the user before invocation by any consumer (the Claude plugin enforces this; humans should too).

The wrapper itself never has a `--force` shortcut, an alias for a destructive operation, or a "convenience" flag that turns one into the other.

## 2. No secrets on disk

Bearer tokens live in macOS Keychain (`security add-generic-password`). The wrapper reads them at call time and never persists them anywhere — not env, not config files, not cache.

If a future port targets a non-macOS platform, the equivalent OS keystore (Linux: `secret-tool`; Windows: `cmdkey`) is the bar. Plain-text fallbacks are not acceptable.

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

`clawctl` is one bash file. Hard dependencies: `bash`, `curl`, `openssl`, `security` (macOS Keychain), and a POSIX `perl` (for the redactor — already on macOS). Optional: `jq` for output filtering, `gh` for `clawctl verify`.

No npm, no Python venv, no homebrew-only deps beyond what `brew bundle` would install in a fresh shell. If a feature needs a heavier runtime, it lives in a separate repo.
