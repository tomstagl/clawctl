# Recipes

Workflow-shaped patterns. Each recipe assumes `CLAWCTL_HOST` is set and `clawctl health` passes. Recipes are intentionally short — they're meant to be read once, then run.

## "Is the gateway alive and what's it serving?"

```bash
clawctl health                                                 # gateway HTTP liveness
clawctl models | jq -r '.data[].id'                            # registered agents (60s cached)
clawctl cli status --json | jq '{ok, providers, channels}'     # readiness summary (host-side)
```

Stop here unless something looks wrong.

## "Talk to a specific agent and capture the trace-id."

```bash
# stderr will print `trace-id: <32-hex>`; capture it for follow-up
clawctl msg -s "session:thread-42" review-bot "Look at PR owner/repo#123 and report risk."
```

If the agent might run >60s, raise `CLAWCTL_TIMEOUT` for that call:

```bash
CLAWCTL_TIMEOUT=300 clawctl msg review-bot "<long task>"
```

Don't stream unless you actually want progressive output — the wrapper buffers either way.

## "Find an agent's last few sessions."

```bash
clawctl cli sessions --agent ops --json --limit 10 \
  | jq -r '.[] | "\(.lastActivity)\t\(.key)\t\(.messageCount // "?")"'
```

For only active threads in the last 2 hours, add `--active 120`.

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

Mutating — confirm before running. Pair with `--announce --channel <name> --to <id>` only after you've named a destination.

## "Investigate a stuck or failed run."

1. List running tasks for the agent:
   ```bash
   clawctl cli tasks list --status running --json \
     | jq -r '.[] | "\(.id)\t\(.runtime)\t\(.startedAt)\t\(.lookup)"'
   ```
2. Inspect one:
   ```bash
   clawctl cli tasks show <lookup> --json
   ```
3. Pull recent gateway logs around the same window:
   ```bash
   clawctl cli logs --limit 500 --json --local-time \
     | jq -r '.ts + " " + .level + " " + .msg' | tail -200
   ```
4. Trace it:
   ```bash
   clawctl trace <32-hex-trace-id>
   ```
5. If the run is genuinely lost: `clawctl cli tasks cancel <lookup>`. Never auto-cancel.

## "Recover from a redaction warning."

The wrapper prints `WARNING: redacted secret pattern(s): <kinds> (agent=<slug>)` when an agent leaks a secret.

1. Note the kind (`dt0c01`, `gh_token`, `aws_akid`, `jwt`, `gw_token`, `brave`) and the agent.
2. Read the audit log: `~/.cache/clawctl/last-redaction`.
3. **Rotate the matching credential immediately.** A redaction hit means the secret was already present in the agent's response, regardless of whether the wrapper masked it from your terminal.
4. Open an issue against that agent's repo with the trace-id (NOT the secret) so the prompt can be hardened.

Never disable redaction (`CLAWCTL_NO_REDACT=1`) outside a one-shot debug session.

## "Add a new openclaw skill to an agent."

```bash
clawctl cli skills search "<query>" --json --limit 20 \
  | jq -r '.[] | "\(.slug)\t\(.version)\t\(.summary)"'
clawctl cli skills info <slug> --json
clawctl cli skills install <slug> --version <pinned-version> --agent <id>
clawctl cli skills check --agent <id> --json | jq '.eligible'
```

Pin versions. Never run `--force` without an explicit reason.

## "Rotate the gateway token."

```bash
clawctl cli doctor --generate-gateway-token             # mutating — rotates + writes new value on the host
# refresh the keychain entry on each client:
security delete-generic-password -s openclaw-gateway-token -a "$USER" 2>/dev/null
security add-generic-password -s openclaw-gateway-token -a "$USER" -w "<new-token>"
clawctl health                                          # confirm transport works again
```

Rotation invalidates every other client's bearer. Coordinate.

## "Inspect what a hook or plugin will do before enabling it."

```bash
clawctl cli hooks list --verbose --json
clawctl cli hooks info <name>
clawctl cli plugins inspect <name> --runtime --json
clawctl cli plugins update --all --dry-run             # preview only
```

Then `clawctl cli hooks enable <name>` or `clawctl cli plugins update <name>` after confirmation.

## "Open the same trace in Jaeger that the gateway is logging."

```bash
clawctl trace <32-hex-trace-id>
# prints: trace-id, UI URL, plus first 30 spans (service / op / dur)
```

Set `CLAWCTL_JAEGER_UI` to point the trace links at your Jaeger instance.

## "Verify a citation an agent put into a GitHub issue/PR body."

```bash
clawctl verify commit  <40-hex-sha>
clawctl verify pr      owner/repo#123
clawctl verify issue   owner/repo#45
clawctl verify file    src/lib/foo.ts                # working tree
clawctl verify file    src/lib/foo.ts main           # at ref
```

Exit `0` verified, `1` not found, `2` usage. Use these before trusting an agent's claim.

## "Read the gateway's last hour of activity without flooding the chat."

```bash
clawctl cli logs --limit 200 --local-time --plain | tail -80
clawctl cli gateway stability --limit 25 --json | jq -r '.[] | "\(.ts)\t\(.type)\t\(.summary)"'
```

For a heavier dump, export the diagnostics zip and share the path (not the contents):

```bash
clawctl cli gateway diagnostics export --output /tmp/openclaw-diag-$(date +%Y%m%dT%H%M%S).zip
```

## "Filter openclaw-authored GitHub issues across multiple repos."

If you follow the loop-back convention from the [`openclaw-loopback`](../skills/openclaw-loopback/SKILL.md) skill bundled with this repo (each agent labels its issues with `openclaw` + `openclaw:<slug>`):

```bash
gh issue list --repo owner/repo-a -l openclaw --limit 50
gh issue list --repo owner/repo-b -l "openclaw:dead-code-sweep" --limit 50
```

Each issue's body starts with a YAML deliverable header (`agent`, `run-id`, `traceparent`, `started`, `ended`, `status`).
