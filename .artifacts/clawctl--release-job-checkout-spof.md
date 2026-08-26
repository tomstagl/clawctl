# PR: fix(release): drop needless checkout from release job, extend artifact retention

**Branch:** `fix/release-job-checkout-spof` → `main`

## Summary

- Removed the `actions/checkout@v4` step from the `release` job in
  `.github/workflows/release.yml`. That job's only work is downloading build
  artifacts, computing checksums, and running `gh release create` — none of
  it touches the repository working tree. Added `GH_REPO: ${{ github.repository }}`
  to the `create release` step's env so `gh` can resolve the target repo
  without a checkout.
- Raised `retention-days` on the four `upload artifact` steps in the `build`
  job from `7` to `30`, so a `release` job that fails for an unrelated reason
  stays re-runnable for a month instead of a week.

## Why

Run `26331142189` (tag `v0.2.4`, 2026-05-23) is the concrete case: every
upstream job (`actionlint`, `test`, `mocked-tests`, all four `build` matrix
jobs) succeeded and all four binaries were uploaded as workflow artifacts,
but the `release` job's `actions/checkout@v4` step failed after 35s, which
skipped every remaining step (`download artifacts`, `list artifacts`,
`generate checksums`, `create release`). Tag `v0.2.4` exists on `main`; no
GitHub release for it exists. The 7-day artifact retention window has since
expired, so that specific release can no longer be recovered by re-running
the job — see `.artifacts/clawctl--v0-2-4-missing-release.md` if present in
the repo history.

The checkout bought nothing for this job and was a single point of failure
that has already lost a release. Removing it, and extending retention so a
future failure of any kind leaves more time to recover via re-run, addresses
both the immediate defect and the recurrence risk.

## Test plan

- [x] `actionlint` run locally against the edited workflow file — passes clean.
- [ ] CI `actionlint` job (same gate, unmodified) passes on the PR.
- [ ] CI `test`, `mocked-tests`, and `build` matrix jobs pass unmodified (no
      changes made to those jobs).
- Not modified/out of scope per task instructions: `test`, `mocked-tests`,
  `build` jobs, `ci.yml`, `nightly.yml`, and the `continue-on-error` +
  aggregate-check pattern in `mocked-tests`.
- No tags pushed, no releases created, no existing tags moved or deleted as
  part of this change.
