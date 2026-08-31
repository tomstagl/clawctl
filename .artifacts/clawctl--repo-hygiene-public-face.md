# PR: repo-hygiene-public-face

Branch: `repo-hygiene-public-face` (pushed, tip `810ea05`)
Base: `main`
Closes: #14

## Title

chore: untrack committed binary and private agent-harness scratch files (#14)

## Body

## Scope

`main` was tracking a 12.3 MB compiled binary and the private `ralph` agent-loop
harness, both shipped to every clone. This PR untracks them going forward.

**Untracked and added to `.gitignore`:**

| Path | What it is |
| --- | --- |
| `clawctl-local` | compiled Mach-O/ELF binary (12,283,648 B) |
| `ralph.sh` | private agent-loop driver |
| `prompt.md` | agent loop prompt |
| `progress.txt` | agent scratch log |
| `.last-branch` | agent scratch state |

`git rm --cached` only — no history rewrite, no force-push. The 12 MB blob stays
in history, per the issue's explicit instruction. All five files are still
present in the working tree; they're just no longer tracked or re-added.

## Recommendation on item 2 (untrack vs. `dev/`)

The issue asked for a recommendation rather than a decision: untrack the ralph
harness files entirely, or move them under a tracked `dev/` directory.

**Recommendation: untrack (implemented here), not `dev/`.**

The issue's own goal is "stop shipping ... the private agent harness to every
clone." Moving the harness to a tracked `dev/` directory doesn't achieve that —
it would still be tracked and still ship to every `git clone`, just under a new
path. Untracking is also the same treatment already applied to `clawctl-local`
in this same PR and to `/cmd/clawctl/clawctl` in the existing `.gitignore`, so
it keeps one consistent rule ("build/scratch artifacts aren't tracked") instead
of two.

**If Tom prefers `dev/` instead:** it's a small follow-up — `git mv ralph.sh
prompt.md progress.txt .last-branch dev/`, drop the corresponding lines from
`.gitignore`, and `git add dev/`. Happy to do that in this PR or a follow-up if
preferred over merging as-is.

## Reference check (item 3)

`grep -rn` across `README.md`, `CONTRIBUTING.md`, `docs/`, `install/`, `test/`,
and `.github/workflows/` for the five removed paths found exactly one hit:

- `docs/typed-binary-language.md:48` — mentions `ralph.sh` stays bash. This
  reference is still accurate: the file isn't renamed or deleted, only
  untracked, so no change was needed. (Per the issue, `docs/` is otherwise
  out of scope for this PR.)

## Stale merged branches (item 4 — reporting only, not deleting)

Both still exist on the remote and are safe to delete (already merged):

- `claude/release-v0.3.0` — merged via PR #4 (2026-06-04)
- `ralph/full-test-coverage` — merged via PR #2 (2026-05-18)

## Verification

- `go vet ./...` — clean
- `go build -o /tmp/... ./cmd/clawctl` — builds
- `bash -n ./clawctl.bash ./install/install.sh` — parses
- Confirmed via `git status` that the five files remain on disk, untracked

## Out of scope (per issue)

No history rewrite, no branch deletion, no touching `CHANGELOG.md` /
`CONTRIBUTING.md` / `prd.json` / `tasks/`, no tag or release.
