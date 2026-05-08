---
description: Curated openclaw workflow recipes — cron, sessions, redaction recovery, debugging stuck runs, token rotation.
---

Workflow-shaped patterns for openclaw, driven through the `clawctl` wrapper. Each recipe assumes `CLAWCTL_HOST` is set and `clawctl health` passes.

Task: $ARGUMENTS

## "Is the gateway alive and what's it serving?"

```bash
clawctl health
clawctl models | jq -r '.data[].id'
clawctl cli status --json | jq '{ok, providers, channels}'
```

Stop here unless something looks wrong.

## "Talk to a specific agent and capture the trace-id."

```bash
# stderr will print `trace-id: <32-hex>`; capture it for follow-up
clawctl msg -s "session:thread-42" review-bot "Look at PR owner/repo#123 and report risk."
```

For agents likely to run >60s: `CLAWCTL_TIMEOUT=300 clawctl msg ...`. Don't stream unless progressive output is genuinely needed — the wrapper buffers either way.

## "Find an agent's last few sessions."

```bash
clawctl cli sessions --agent ops --json --limit 10 \
  | jq -r '.[] | "\(.lastActivity)\t\(.key)\t\(.messageCount // "?")"'
```

Add `--active 120` to filter to the last 2 hours of activity.

## "Schedule a daily brief at 07:00 local time."

```bash
clawctl cli cron add \
  --name "daily-brief" \
  --cron "0 7 * * *" \
  --tz "Europe/Vienna" \
  --agent ops \
  --session isolated \
  --light-context \
  --message "Summarize overnight openclaw runs and surface anomalies."
clawctl cli cron list --agent ops --json | jq -r '.[] | "\(.id)\t\(.cron)\t\(.name)"'
```

**Mutating** — confirm before running. Add `--announce --channel <name> --to <id>` only after a destination is named.

## "Investigate a stuck or failed run."

1. Get running tasks:
   ```bash
   clawctl cli tasks list --status running --json \
     | jq -r '.[] | "\(.id)\t\(.runtime)\t\(.startedAt)\t\(.lookup)"'
   ```
2. Inspect one: `clawctl cli tasks show <lookup> --json`
3. Pull recent gateway logs:
   ```bash
   clawctl cli logs --limit 500 --json --local-time \
     | jq -r '.ts + " " + .level + " " + .msg' | tail -200
   ```
4. Trace it: `clawctl trace <32-hex-trace-id>`
5. If genuinely lost: confirm with the user, then `clawctl cli tasks cancel <lookup>`. **Never** auto-cancel.

## "Recover from a redaction warning."

When the wrapper prints `WARNING: redacted secret pattern(s): <kinds> (agent=<slug>)`:

1. Note the kind (`dt0c01`, `gh_token`, `aws_akid`, `jwt`, `gw_token`, `brave`) and the agent.
2. Read the audit log: `~/.cache/clawctl/last-redaction`.
3. **Rotate the matching credential immediately.** A redaction hit means the secret was already present in the agent's response.
4. Open an issue tagged `openclaw:<slug>` against the agent's repo with the trace-id (NOT the secret) so the prompt can be hardened.

Never disable redaction (`CLAWCTL_NO_REDACT=1`) outside a one-shot debug session.

## "Add an openclaw skill to an agent."

```bash
clawctl cli skills search "<query>" --json --limit 20 \
  | jq -r '.[] | "\(.slug)\t\(.version)\t\(.summary)"'
clawctl cli skills info <slug> --json
clawctl cli skills install <slug> --version <pinned-version> --agent <id>
clawctl cli skills check --agent <id> --json | jq '.eligible'
```

**Mutating** — confirm install. Pin versions; never run `--force` without explicit user direction.

## "Rotate the gateway token."

```bash
clawctl cli doctor --generate-gateway-token             # rotates + writes new value on the host
security delete-generic-password -s openclaw-gateway-token -a "$USER" 2>/dev/null
security add-generic-password -s openclaw-gateway-token -a "$USER" -w "<new-token>"
clawctl health                                          # confirm transport works
```

**Mutating** — confirm before running. Rotation invalidates every other client's bearer.

## "Inspect a hook or plugin before enabling it."

```bash
clawctl cli hooks list --verbose --json
clawctl cli hooks info <name>
clawctl cli plugins inspect <name> --runtime --json
clawctl cli plugins update --all --dry-run
```

Then `clawctl cli hooks enable <name>` or `clawctl cli plugins update <name>` after confirmation.

## "Open the same trace in Jaeger."

```bash
clawctl trace <32-hex-trace-id>
```

Set `CLAWCTL_JAEGER_UI` so the link points at your Jaeger UI.

## "Verify a citation an agent put into a GitHub issue/PR body."

```bash
clawctl verify commit  <40-hex-sha>
clawctl verify pr      owner/repo#123
clawctl verify issue   owner/repo#45
clawctl verify file    src/lib/foo.ts
clawctl verify file    src/lib/foo.ts main
```

Exit `0` verified, `1` not found, `2` usage. Run before trusting an agent's claim.

## "Read recent gateway activity without flooding chat."

```bash
clawctl cli logs --limit 200 --local-time --plain | tail -80
clawctl cli gateway stability --limit 25 --json | jq -r '.[] | "\(.ts)\t\(.type)\t\(.summary)"'
```

For a heavier dump, export the diagnostics zip and **share the path, not the contents**:

```bash
clawctl cli gateway diagnostics export --output /tmp/openclaw-diag-$(date +%Y%m%dT%H%M%S).zip
```

## "Filter openclaw-authored issues across repos."

If your fleet follows the loop-back convention (see the `openclaw-loopback` skill):

```bash
gh issue list --repo owner/repo-a -l openclaw --limit 50
gh issue list --repo owner/repo-b -l "openclaw:dead-code-sweep" --limit 50
```

Each issue's body opens with a YAML deliverable header (`agent`, `run-id`, `traceparent`, `started`, `ended`, `status`).
