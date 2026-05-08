---
name: openclaw-loopback
description: Conventions for openclaw agents that deliver work as GitHub issues / PRs. Covers the label scheme, YAML deliverable header, R-1..R-12 communication-layer rules, and the new-agent checklist. Trigger when adding an openclaw agent, reading openclaw-authored GitHub artifacts, or auditing the loopback contract.
---

# OpenClaw GitHub loop-back

OpenClaw agents that touch a codebase deliver their work by opening GitHub issues or pull requests directly from their workspace. This skill captures the conventions every agent — and every reviewer of an agent's output — should follow.

## Repos in scope

This convention is generic. Apply it to any repo that runs openclaw agents. In the reference implementation it covers two:

- `<owner>/<primary-repo>`
- `<owner>/<secondary-repo>` (if applicable)

## Loop-back design (agents → repo)

Scheduled openclaw agents end their runs by invoking `gh` directly from their workspace and opening one issue or PR per run. Do **not** route agent output through a webhook bridge or the openclaw hooks plugin unless cross-cutting structured payloads become a real requirement.

A run that produced no GitHub artifact is treated as a silent drop. Agent prompts must include a failsafe that opens a `BLOCKED` issue rather than dying silently.

## Label conventions

- `openclaw` (color suggestion: `#ff7e29`) — catch-all, applied to every agent-generated issue or PR.
- `openclaw:<slug>` — per-agent, where `<slug>` matches the agent's openclaw ID (e.g. `openclaw:dead-code-sweep`). One label per agent.
- The agent's run-id goes in the issue/PR body as parseable frontmatter (`run-id: <uuid>`), **not** as a label. Per-run-id labels would create thousands and are forbidden.

Discoverability:

- All agent activity: `gh issue list -l openclaw`
- One agent's activity: `gh pr list -l openclaw:<slug>`

## YAML deliverable header

Every issue or PR an openclaw agent opens MUST start with a fenced YAML block:

```yaml
---
agent: <slug>
run-id: <uuid>
traceparent: 00-<32-hex>-<16-hex>-01
started: <ISO 8601 UTC>
ended: <ISO 8601 UTC>
status: DONE | PARTIAL | BLOCKED
---
```

This is the contract for the loop-back.

## GitHub auth requirements

Whichever account runs `gh` on the openclaw host needs at minimum:

- `repo` — issues, PRs, labels.
- `workflow` — required by agents that dispatch GitHub Actions.

If you add an agent that triggers Actions, verify `gh auth status` includes `workflow` before merging.

## Communication-layer rules (R-1..R-12)

These apply to every interaction between the wrapper and the gateway, and to every agent that produces a GitHub artifact.

### R-1 — Citations are first-class

Every claim an agent makes in an issue/PR must be paired with a verifiable citation: a commit SHA, a `<owner>/<repo>#<num>` reference, a file path (optionally at a ref), or a trace-id. Reviewers verify with `clawctl verify`.

### R-2 — Citations must resolve

| Claim         | Cite as            | Verify with                        |
| ------------- | ------------------ | ---------------------------------- |
| Commit        | `<40-hex sha>`     | `clawctl verify commit <sha>`           |
| PR            | `owner/repo#<num>` | `clawctl verify pr owner/repo#<num>`    |
| Issue         | `owner/repo#<num>` | `clawctl verify issue owner/repo#<num>` |
| File at HEAD  | `<path>`           | `clawctl verify file <path>`            |
| File at a ref | `<path>@<ref>`     | `clawctl verify file <path> <ref>`      |

Exit `0` on a successful citation, `1` if the citation does not resolve, `2` on usage errors.

### R-8 — No silent drops

A run that produced no GitHub artifact is treated as a silent drop. Prompts must include a failsafe that opens a `BLOCKED` issue with the YAML header and an explanation. Counts as an R-8 violation if missing.

### R-9 — Slug discipline

Slugs are stable, lowercase, hyphen-separated (`dead-code-sweep`, never `DeadCodeSweep`). When an agent reports as "Agent N (Foo)", check the agent charter file (typically `docs/openclaw/agents.md` or equivalent), not memory of an earlier conversation.

### R-11 — Show paths, never values

Cite paths/refs/IDs. Never paste secret material into an issue/PR body — even partial. The wrapper redacts at the boundary, but agents must not rely on that. A redaction warning at the wrapper layer means the agent already failed R-11 upstream, and the matching credential should be rotated.

### R-12 — Two-layer secret redaction

Even with R-11 in force on the agent side, the wrapper enforces redaction at the boundary:

1. **Wrapper layer (`clawctl msg`, `clawctl stream`)**: post-response regex filter for `dt0c01.*`, `dt0s16.*`, `gh[ps]_*`, `AKIA[0-9A-Z]{16}`, JWTs, and the gateway-token literal. Any hit is replaced with `<REDACTED:kind:first-11-chars…>` and audited at `~/.cache/clawctl/last-redaction`.
2. **Gateway layer**: `logging.redactPatterns` in the gateway config masks the same shapes in the on-disk gateway logs.

Either layer firing means an agent violated R-11 — rotate the matching credential immediately.

## Adding a new agent — checklist

1. Choose a stable lowercase slug (e.g. `dead-code-sweep`).
2. Configure the openclaw cron job with:
   - `--session isolated` — fresh transcript per run.
   - `--tools <subset>` — restrict the capability surface.
   - A prompt that mandates the labels above and the YAML deliverable header.
3. The first issue/PR may need to create the `openclaw:<slug>` label on the fly (`gh label create` if missing).
4. Add a row in `docs/openclaw/agents.md` (or equivalent) with cron + max tool surface.
5. Verify `gh auth status` includes the right scopes (`repo`, plus `workflow` if needed).

## Driving openclaw from a Claude Code session

Use the `/clawctl` slash command (also in this plugin). It wraps the `clawctl` CLI kubectl-style: read-only by default, JSON-first, traceparent on every call, redaction at the boundary.
