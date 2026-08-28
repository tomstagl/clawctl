TITLE: backfill: v0.2.4 binaries + drafted release notes (recommend Option B — see body)

BODY:
Closes evidence-gathering for #7. **This PR does not publish anything** — no
`gh release create`, no tag pushed/moved/deleted. It only adds build artifacts and
drafted notes to `.artifacts/v0.2.4/` for the owner to review, per issue #7's ceiling
("Drafting is this task's ceiling").

## The gap

Tag `v0.2.4` (`ed3b10e4d4ee7af1878486939364d2389b7a2360`, pushed 2026-05-23) is an
ancestor of `main` but has no GitHub Release — the `release` job's checkout failed at
the time, and the workflow-artifact binaries later expired under `retention-days: 7`.
Every other tag (`v0.1.0` through `v0.3.0`) has a published release; `v0.2.4` is the
only gap.

## What's in this PR (Option A prep, done regardless of which option is chosen)

`.artifacts/v0.2.4/` contains binaries built from `ed3b10e4` with the exact flags
`release.yml`'s `build` job uses:

```
CGO_ENABLED=0, -trimpath
-ldflags "-s -w -X main.version=v0.2.4 -X main.commit=ed3b10e4d4ee7af1878486939364d2389b7a2360"
matrix: darwin/{arm64,amd64} + linux/{arm64,amd64}
```

- `clawctl-darwin-arm64`, `clawctl-darwin-amd64`, `clawctl-linux-amd64`, `clawctl-linux-arm64`
- `SHA256SUMS`
- `RELEASE_NOTES.md` — drafted notes for `v0.2.3...v0.2.4`

`go vet ./...` and `go test ./...` were run against the `ed3b10e4` tree first and pass.
`clawctl-linux-amd64 version` was smoke-tested and prints
`clawctl v0.2.4 (ed3b10e4d4ee7af1878486939364d2389b7a2360)` — matching install.sh's
version sentinel.

## The decision — owner's call, not mine

**Option A — backfill the release.** Use the artifacts in this PR: create the `v0.2.4`
GitHub Release manually (or via a one-off `gh release create`) with these binaries,
`SHA256SUMS`, and the drafted notes. Straightforward since the binaries are already
built and byte-identical to what CI would have produced.

**Option B — record the gap instead.** `v0.2.4`'s only content is one commit (skill
file + installer changes — see `.artifacts/v0.2.4/RELEASE_NOTES.md` for the full diff
summary), and it's superseded twelve days later by `v0.3.0`, which is already the
current release. `CHANGELOG.md` doesn't carry individual entries for `v0.2.1`–`v0.2.4`
either (it jumps from `v0.1.0` straight to `v0.3.0`), so the project isn't treating
these patch tags as independently notable. A short `CHANGELOG.md` note ("`v0.2.4` was
tagged but never released; see `v0.3.0` for what shipped next") would prevent confusion
for a reader comparing `v0.2.3` to `v0.3.0`, at much lower ongoing cost than maintaining
a release for a tag nobody currently depends on.

**My recommendation: Option B.** The backfilled binaries are ready in this PR if you'd
rather have them published, but given `v0.2.4` was superseded within two weeks and the
changelog already treats the `v0.2.x` line as undocumented individually, I don't think
publishing a release for it now adds much beyond closing a cosmetic gap — a changelog
note does that more cheaply. Either way, this PR gives you what's needed to execute
whichever you pick.

## Not done here (per issue #7's "Do not" list)

- No `gh release create`, no release published.
- No tag re-pushed, moved, or deleted.
- No empty commit / indirect CI re-trigger.
- No general release-process refactor (the `release` job's checkout failure is a
  separate task).
- Not merging this PR myself.
