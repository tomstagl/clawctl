# v0.2.4 (draft — not published)

Tag `v0.2.4` (`ed3b10e4d4ee7af1878486939364d2389b7a2360`) was pushed on 2026-05-23 but
the `release` job's checkout step failed, so no GitHub Release was ever created and the
workflow-artifact binaries expired under `retention-days: 7`. This is a backfill built
directly from the tagged commit, reproducing `release.yml`'s `build` job exactly:

```
CGO_ENABLED=0, -trimpath
-ldflags "-s -w -X main.version=v0.2.4 -X main.commit=ed3b10e4d4ee7af1878486939364d2389b7a2360"
matrix: darwin/{arm64,amd64} + linux/{arm64,amd64}
```

## What's Changed

Single commit over `v0.2.3`:

* feat(skill): add clawctl skill; installer registers skill + MCP globally (`ed3b10e4`)
  - Adds `skills/clawctl/SKILL.md`, a tight action-oriented reference teaching Claude how
    to use every clawctl command, when to prefer HTTP over SSH, output formats, env vars,
    and exit codes.
  - `install/install.sh` now also downloads the skill to `~/.claude/skills/clawctl/SKILL.md`
    (always fetches latest from `main`) and registers the clawctl MCP server at user scope
    via `claude mcp add`.
  - Bumps plugin version to 0.2.3 in `.claude-plugin/plugin.json` and
    `.claude-plugin/marketplace.json`.

**Full Changelog**: https://github.com/tomstagl/clawctl/compare/v0.2.3...v0.2.4

## Binaries

Built and checksummed in this backfill (see `SHA256SUMS` in this directory):

- `clawctl-darwin-arm64`
- `clawctl-darwin-amd64`
- `clawctl-linux-amd64`
- `clawctl-linux-arm64`

## If the owner chooses to publish this

This directory's contents (`clawctl-*`, `SHA256SUMS`) are exactly what `release.yml`'s
`build` job would have produced and uploaded to a `v0.2.4` GitHub Release. Publishing
still requires `gh release create v0.2.4 --title v0.2.4 --notes-file ... .artifacts/v0.2.4/clawctl-* .artifacts/v0.2.4/SHA256SUMS`
(or equivalent), which this task does not do — see issue #7's "Do not" list.
