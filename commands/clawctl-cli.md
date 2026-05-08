---
description: Full openclaw CLI reference — every subcommand reachable via `clawctl cli ...`, with flags and examples.
---

Compact reference for every openclaw subcommand reachable via `clawctl cli <command>`. Source: `https://docs.openclaw.ai/cli/<name>`. Always pass `--json` when filtering output with `jq`.

Task: $ARGUMENTS

Use this only when the `/clawctl` and `/clawctl-recipes` slash commands don't already answer the question. Each section lists subcommands → flags → examples in the form the docs publish.

> **Note**: this reference mirrors the upstream openclaw docs. If a flag has changed in your installed version, check `clawctl cli <subcommand> --help` and report the discrepancy upstream.

---

## `agents` — agent lifecycle and channel routing

Subcommands: `list`, `add [name]`, `bindings`, `bind`, `unbind`, `delete <id>`, `set-identity`.

Flags: `--json`, `--bindings`, `--workspace <dir>`, `--model <id>`, `--agent-dir <dir>`, `--bind <channel[:accountId]>`, `--non-interactive`, `--agent <id>`, `--all`, `--force`, `--identity-file <path>`, `--from-identity`, `--name`, `--theme`, `--emoji`, `--avatar`.

```bash
clawctl cli agents list --json
clawctl cli agents list --bindings
clawctl cli agents add work --workspace ~/.openclaw/workspace-work
clawctl cli agents add ops --workspace ~/.openclaw/workspace-ops --bind telegram:ops --non-interactive
clawctl cli agents bindings --agent work --json
clawctl cli agents bind --agent work --bind telegram:ops --bind discord:guild-a
clawctl cli agents unbind --agent work --bind telegram:ops
clawctl cli agents unbind --agent work --all
clawctl cli agents set-identity --workspace ~/.openclaw/workspace --from-identity
clawctl cli agents set-identity --agent main --name "OpenClaw" --emoji ":lobster:" --avatar avatars/openclaw.png
clawctl cli agents delete work
```

**Mutating**: `add`, `bind`, `unbind`, `delete`, `set-identity`. Confirm before running.

---

## `cron` — recurring + one-shot scheduled runs

Subcommands: `list`, `show <job-id>`, `add`, `edit <job-id>`, `run <job-id>`, `runs --id <job-id>`.

Schedule flags: `--at <datetime>`, `--tz <iana>`, `--keep-after-run`, `--cron "<expression>"`.
Session flags: `--session main|isolated|current|session:<id>`.
Delivery flags: `--announce`, `--no-deliver`, `--channel <name>`, `--to <target>`, `--thread-id <id>`, `--best-effort-deliver`, `--no-best-effort-deliver`.
Job flags: `--name <text>`, `--message <text>`, `--agent <id>`, `--clear-agent`, `--model <ref>`, `--light-context`.

```bash
# List
clawctl cli cron list --json --limit 20
clawctl cli cron list --agent ops --json
clawctl cli cron show <job-id> --json

# Add (recurring)
clawctl cli cron add \
  --name "daily-brief" \
  --cron "0 7 * * *" --tz "Europe/Vienna" \
  --agent ops --session isolated --light-context \
  --message "Summarize overnight openclaw runs."

# Add (one-shot)
clawctl cli cron add \
  --name "remind-deploy" \
  --at "2026-05-10T09:00" --tz "Europe/Vienna" \
  --agent ops \
  --message "Remind me about the deploy window."

# Run on demand
clawctl cli cron run <job-id>
clawctl cli cron runs --id <job-id> --json --limit 5
```

**Mutating**: `add`, `edit`, `run`. Confirm.

---

## `sessions` — conversation threads

```bash
clawctl cli sessions --agent ops --json --limit 10
clawctl cli sessions --all-agents --json --active 120
clawctl cli sessions --agent ops --key "session:thread-42" --json
clawctl cli sessions cleanup --dry-run --older-than 30d
clawctl cli sessions cleanup --enforce --older-than 30d        # mutating
```

**Mutating**: `cleanup --enforce`.

---

## `skills` — skill marketplace + per-agent installs

```bash
clawctl cli skills search "<query>" --json --limit 20
clawctl cli skills info <slug> --json
clawctl cli skills installed --agent <id> --json
clawctl cli skills install <slug> --version <pin> --agent <id>     # mutating
clawctl cli skills update <slug> --agent <id>                       # mutating
clawctl cli skills uninstall <slug> --agent <id>                    # mutating
clawctl cli skills check --agent <id> --json
```

**Mutating**: `install`, `update`, `uninstall`.

---

## `hooks` — gateway lifecycle hooks

```bash
clawctl cli hooks list --verbose --json
clawctl cli hooks info <name>
clawctl cli hooks enable <name>                                     # mutating
clawctl cli hooks disable <name>                                    # mutating
```

---

## `channels` — Discord / Telegram / Slack / etc.

```bash
clawctl cli channels list --json
clawctl cli channels add <name> --kind telegram --json              # mutating
clawctl cli channels login <name>                                   # mutating
clawctl cli channels logout <name>                                  # mutating
clawctl cli channels remove <name>                                  # mutating
```

---

## `approvals` — confirmation policy for mutating ops

```bash
clawctl cli approvals show --json
clawctl cli approvals set --policy strict                            # mutating
clawctl cli approvals allowlist --add "skills install"               # mutating
```

---

## `secrets` — provider keys and credentials

```bash
clawctl cli secrets list --json
clawctl cli secrets configure <provider>                             # mutating
clawctl cli secrets apply --provider openai                          # mutating
clawctl cli secrets remove <name>                                    # mutating
```

---

## `status` — gateway readiness summary

```bash
clawctl cli status --json | jq '{ok, providers, channels, gateway}'
```

Read-only.

---

## `models` — registered model identities (host view)

```bash
clawctl cli models list --json
```

Read-only. The HTTP equivalent (`clawctl models`) is what most callers want — it's cached.

---

## `mcp` — Model Context Protocol servers

```bash
clawctl cli mcp list --json
clawctl cli mcp install <slug>                                       # mutating
clawctl cli mcp remove <slug>                                        # mutating
```

---

## `tasks` — running and recent task records

```bash
clawctl cli tasks list --status running --json
clawctl cli tasks list --agent ops --limit 25 --json
clawctl cli tasks show <lookup> --json
clawctl cli tasks cancel <lookup>                                    # mutating
```

---

## `plugins` — gateway-side plugins

```bash
clawctl cli plugins list --json
clawctl cli plugins inspect <name> --runtime --json
clawctl cli plugins install <name>                                   # mutating
clawctl cli plugins update <name>                                    # mutating
clawctl cli plugins update --all --dry-run
clawctl cli plugins uninstall <name>                                 # mutating
```

---

## `dns` — gateway hostname resolution

```bash
clawctl cli dns show --json
clawctl cli dns set --host openclaw.local                            # mutating
```

---

## `gateway` — process and diagnostics

```bash
clawctl cli gateway stability --limit 25 --json
clawctl cli gateway diagnostics export --output /tmp/openclaw-diag.zip
clawctl cli gateway start                                            # mutating
clawctl cli gateway stop                                             # mutating
clawctl cli gateway restart                                          # mutating
clawctl cli gateway install                                          # mutating
clawctl cli gateway uninstall                                        # mutating
```

---

## `doctor` — health checks and remediation

```bash
clawctl cli doctor --json
clawctl cli doctor --repair --dry-run
clawctl cli doctor --repair                                          # mutating
clawctl cli doctor --generate-gateway-token                          # mutating, rotates token
```

---

## `logs` — gateway log tail

```bash
clawctl cli logs --limit 200 --local-time --plain | tail -80
clawctl cli logs --limit 500 --json --local-time \
  | jq -r '.ts + " " + .level + " " + .msg' | tail -200
```

Read-only.

---

## Conventions

- Always pass `--json` when piping to `jq`.
- Mutating commands are tagged in this reference. Confirm with the user before invoking.
- Trace-id from stderr is the right thing to cite when reporting an issue, not the body.
