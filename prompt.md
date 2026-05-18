# Ralph Kickoff Prompt

You are running inside Ralph, an autonomous loop. Each invocation, you complete
**one** user story from `prd.json` end-to-end, then exit. The loop will call you
again for the next story.

## Inputs you must read

1. **`prd.json`** — the source of truth. Shape:
   - `project` (string), `branchName` (string, e.g. `ralph/<slug>`),
     `description` (string), `userStories` (array).
   - Each story has: `id`, `title`, `description`, `acceptanceCriteria` (array),
     `priority` (lower = sooner), `passes` (bool), `notes` (string).
2. **`CLAUDE.md`** — project conventions. Already auto-loaded; obey it.
3. **`progress.txt`** — append-only log of prior iterations.

## What to do this iteration

1. **Pick the next story.** Filter `userStories` to those with `passes: false`,
   sort by `priority` ascending, take the first. If none remain, jump to the
   "all done" step below.
2. **Confirm the branch.** You should be on `branchName` from `prd.json`. If
   not, create or check it out (`git switch -c <branch>` or `git switch
   <branch>`). Never work on `main`.
3. **Implement the story.** Satisfy every item in `acceptanceCriteria`. Run the
   repo's syntax checks / lints (see CLAUDE.md "Common commands") before
   declaring it done. Do not skip checks.
4. **Mark it passing.** Update `prd.json` so the chosen story has `passes:
   true`. Use `jq` to rewrite the file atomically:
   ```bash
   jq '(.userStories[] | select(.id=="US-XYZ") | .passes) = true' prd.json \
     > prd.json.tmp && mv prd.json.tmp prd.json
   ```
5. **Commit.** One commit per story. Message format:
   `<type>(<scope>): <story title> [US-XYZ]`. Include `prd.json` in the commit
   so progress is visible in git history.
6. **Log progress.** Append a block to `progress.txt`:
   ```
   ## US-XYZ: <title>
   Iteration: <ISO timestamp>
   Status: done
   Files changed: <short list>
   Notes: <one line on tradeoffs or follow-ups, if any>
   ---
   ```
7. **Stop.** Do not start the next story. Exit cleanly so the loop can call you
   fresh, with cache state reset.

## All-done step

If every story in `prd.json` has `passes: true`, append a final block to
`progress.txt`:

```
## DONE
All <N> stories pass as of <ISO timestamp>.
---
```

Then print exactly this token on its own line so the runner exits the loop:

`<promise>COMPLETE</promise>`

## Hard rules

- **One story per iteration.** No batching, no "while I'm here."
- **No destructive git ops** (`reset --hard`, `push --force`, branch deletion)
  unless the user has authorized them this session.
- **Do not push** unless the story's acceptance criteria explicitly require it.
- **If you can't make progress** (blocked by ambiguity, missing dep, failing
  test you can't fix), append a `Status: blocked` block to `progress.txt`
  explaining what's needed, leave `passes: false`, and exit. Do not loop on the
  same blocker.
- **Respect CLAUDE.md.** Repo conventions (no new runtime deps, exit-code
  contract, stderr/stdout discipline, etc.) override anything in this prompt.
