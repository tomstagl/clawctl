# Blog post prompt — `clawctl` release on tomstagl.com

Paste this entire block into a fresh Claude Code session opened at `~/code/tomstagl.com` (or wherever your blog repo lives). The prompt is self-contained — the new session has no memory of the conversation that produced it.

---

You're helping me write a build-in-public blog post for **tomstagl.com**. The post announces and explains a tool I just open-sourced: `clawctl`, a kubectl-style wrapper for the [openclaw](https://openclaw.ai) agent gateway, and its companion Claude Code plugin.

## What you should do

1. Read existing posts in this repo (look in `content/`, `posts/`, or wherever tomstagl.com stores them) to learn my voice. Match it. I write in first person, low ceremony, no marketing fluff, no exclamation marks. Short paragraphs. Concrete over abstract. Honest about trade-offs.
2. Read the source material listed below — the README, design principles, and recipes for `clawctl`, plus the plugin manifest. Don't copy-paste from them; explain *why* the tool exists, not *what every flag does*.
3. Draft one post (~1,200–1,800 words) ready to publish. Output it as a new markdown file in the right directory (look at how other posts are stored). Include the frontmatter format other posts use.
4. After writing, list 3 alternative titles and 3 alternative opening lines so I can pick. Don't pick for me.

## Source material (read these first)

- `~/code/clawctl/README.md` — overview, install, surface (CLI **and** plugin live here)
- `~/code/clawctl/docs/design-principles.md` — the 5 non-negotiable rules
- `~/code/clawctl/docs/recipes.md` — concrete workflows
- `~/code/clawctl/clawctl` — the actual bash script (skim it; one-binary discipline is the story)
- `~/code/clawctl/.claude-plugin/plugin.json` — the Claude Code plugin manifest (same repo)
- `~/code/clawctl/skills/openclaw-loopback/SKILL.md` — the GitHub loop-back convention

## What the post should cover

Lead with the problem, not the tool.

- I run a small openclaw agent fleet (currently 4 registered agents — `default`, `main`, `dead-code-sweep`, `test-coverage-filler`) maintaining recordsv.lt and a sibling Lambda service. Talking to that fleet from my Mac meant juggling bearer tokens, traceparents for observability, regex-redacting leaked secrets out of agent responses, and SSHing for ops commands. Doing that with raw `curl` works once. Doing it 50 times a day is how secrets end up in shell history.
- I built `clawctl` to collapse that into one wrapper. Five rules carry it: read-only by default, no secrets on disk, traceparent on every call, redact at the boundary, one binary with zero runtime deps. Each of those came from a specific failure mode I'd actually hit (or watched a teammate hit elsewhere) — not theory.
- The same repo doubles as a Claude Code plugin. The plugin wraps `clawctl` behind a `/clawctl` slash command and ships a generic GitHub loop-back skill for projects whose agents deliver work as labelled PRs/issues. This is how my fleet talks back: every issue/PR carries a YAML deliverable header (`agent`, `run-id`, `traceparent`, `started`, `ended`, `status`) and dual labels (`openclaw` + `openclaw:<slug>`). One repo, one issue tracker, one release flow — the CLI and the plugin version together.
- The honest framing: I'm a one-person shop building a SaaS (recordsv.lt). The `clawctl` story is also the story of why I'm building the agent fleet at all — minimalist-entrepreneur style, no team, audience as moat. Tools like this come *from* running the business, not *instead of* running it.

## Tone calibration

- Don't oversell. The fleet is small (4 agents, weekly cadence). Don't write as if I'm running a hundred. The interesting thing isn't scale — it's that the rails are well-built enough to scale.
- Mention that the name `clawctl` follows the kubectl/flyctl/roxctl convention — short on purpose, and chosen so it doesn't collide with OpenShift's `oc`.
- No "I'm thrilled to announce." Just say what the tool does, why I built it, and how to install it.
- Code blocks are good but don't pad. A few examples that show the *shape* of the wrapper, not a flag dump.

## What to NOT include

- No flag-by-flag walkthrough — link to `docs/cli-reference.md` for that.
- No openclaw founder-name-checking or industry punditry.
- No "the future of agents is…" speculation.
- No emojis.

## Structure suggestion (you can deviate if a better one suggests itself)

1. **One-paragraph hook**: the problem, in plain language. The 50-times-a-day-curl observation works.
2. **Why this exists**: the five failure modes that became the five design principles.
3. **What it looks like**: 3–4 short examples (`clawctl health`, `clawctl msg`, `clawctl verify`, `clawctl trace`) showing the kubectl-style ergonomics.
4. **The plugin**: how the Claude Code layer enforces the same rules from the other side, plus the GitHub loop-back convention.
5. **Naming + caveats**: the OpenShift `clawctl` collision, the macOS-Keychain-only auth, the bash-only stack. What's intentionally not there.
6. **Install + close**: brew + curl install, what to read next, link to the repo (single repo for both CLI and plugin).

## Deliverable

Write the post directly to the right path with the right frontmatter. Then output:

- The 3 alternative titles.
- The 3 alternative opening lines.
- A 1-line social card description (≤ 200 chars) for previews.

Do **not** ask me to confirm before writing. Just draft and let me edit.

---

End of prompt. The new session will need filesystem access to `~/code/clawctl/` (read-only is fine) and write access to the tomstagl.com repo.
