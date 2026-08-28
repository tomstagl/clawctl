# Contributing

`clawctl` is small and meant to stay small. The full source is one bash file. PRs that grow it should pay for the complexity with a clear use case.

## Before opening a PR

1. Read [`docs/design-principles.md`](docs/design-principles.md). PRs that violate them will be closed.
2. Run `./test/smoke.sh` against your own openclaw gateway. It exercises `clawctl health`, `clawctl models`, redaction, and traceparent format.
3. If you add a command: add a row to the surface table in `README.md` and a recipe in `docs/recipes.md`.
4. If you touch redaction patterns: add a test case in `test/smoke.sh`.

## Running smoke tests

`test/smoke.sh` validates `clawctl` against a live openclaw gateway.

**Without a gateway (syntax + offline checks only):**

```bash
./test/smoke.sh --no-network   # skips all live-network tests, exits 0
```

**Full live suite** — requires `CLAWCTL_HOST` and a valid keychain entry:

```bash
CLAWCTL_HOST=your-gateway.example.com ./test/smoke.sh
```

The CI `smoke-static` job runs only `bash -n` (syntax) and `shellcheck` (lint) on the script — no gateway needed.

## Out of scope

- Non-macOS Keychain support (separate repo / fork).
- Bash → Python/Go rewrite (separate repo / fork).
- Convenience flags that wrap mutating commands without confirmation.
- Anything that disables redaction by default.

## Releasing

1. Tag: `git tag v0.X.0 && git push --tags`. `.github/workflows/release.yml` builds the four platform binaries, generates `SHA256SUMS`, and publishes a GitHub Release — the Go binary's version is stamped at build time via `-ldflags "-X main.version=${GITHUB_REF_NAME}"`, nothing to hand-bump there.
2. Bump the version in `.claude-plugin/plugin.json`.
3. Add a `CHANGELOG.md` entry.
