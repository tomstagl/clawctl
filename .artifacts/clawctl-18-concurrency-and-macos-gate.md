# PR: ci: add concurrency guard, gate macOS install-smoke leg

**Branch:** `ci-concurrency-and-macos-gate` → `main`

## Title

ci: add concurrency guard, gate macOS install-smoke leg on release

## Body

## Summary

GitHub Actions minutes on this account were exhausted on 2026-09-19 (~17:50Z–18:40Z);
every job failed in seconds with no log stream until spend was extended. This closes
out issue #18 (part of epic #17) with the two changes it scopes, both aimed at that
burn rate:

- **Concurrency guard.** `ci.yml` had none, so superseded PR pushes kept running to
  completion instead of being cancelled, and every push queued a fresh full run.
  Added a top-level `concurrency:` block copying the pattern from studio-master's
  `ci.yml` (PR #844): `pull_request` runs are grouped by PR ref with
  `cancel-in-progress: true`; `push` runs are grouped by ref **+ SHA** with
  `cancel-in-progress: false`, so a run for an already-merged commit can never be
  evicted by a later push — only superseded PR runs get cancelled.
- **macOS gate on `install-smoke`.** The job's matrix ran `macos-latest` +
  `ubuntu-latest` on every trigger, but almost all of its steps were already gated
  behind `has_release == 'true'` (checked via `gh release list`) and no-op otherwise.
  macOS runners bill at roughly 10x the Linux rate, so spinning one up to do nothing
  was the more expensive half of this issue despite being the smaller diff. Pulled the
  release check out into its own cheap `release-check` job that runs first, and made
  the `install-smoke` matrix's `os` list conditional on that job's output via
  `fromJSON(...)`. macOS coverage is **not** dropped — it still runs in full whenever
  a release exists to test against, since clawctl's install path needs real
  macOS testing once releases are cut.

## Expected CI behavior on this PR

This PR itself has no release published yet, so on its own run:
- `release-check` runs (cheap, ubuntu-latest, one `gh release list` call).
- `install-smoke` matrix resolves to `["ubuntu-latest"]` only — **no macOS leg is
  created** (not skipped-with-a-red-X, simply absent from the job list).
- The `ubuntu-latest` leg still runs and hits the "Skip (no release published yet)"
  no-op step, same as before this change.
- All other jobs (`actionlint`, `shellcheck`, `syntax`, `smoke-static`, `parity`,
  `envelope`, `contract`, `plugin`, `go`) are unaffected by this change and run as
  before.

Once a release exists (e.g. a future `v*` tag build), `install-smoke`'s matrix
resolves to `["ubuntu-latest", "macos-latest"]` and both legs run their full
install/verify/health-check/redact/plugin-manifest steps, exactly as they did before
this change (just no longer computing the release check redundantly per-leg).

## Validation

Local validation only — no speculative pushes to watch CI, since reducing that spend
is the point of this issue:
- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))"` — valid YAML.
- `go install github.com/rhysd/actionlint/cmd/actionlint@latest && actionlint
  .github/workflows/ci.yml` — clean, no findings (covers the `concurrency:` and
  `fromJSON` matrix expressions).
- Manually traced both matrix branches (`has_release == 'true'` /
  `'false'`) and both concurrency branches (`push` / `pull_request`) against the
  expression logic above.

Closes #18
