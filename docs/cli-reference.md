# openclaw CLI reference

Compact reference for every openclaw subcommand reachable via `clawctl cli <command> ...`. Source: `https://docs.openclaw.ai/cli/<name>`. Include `--json` whenever you intend to filter the output with `jq`.

Read this file only when the [recipes](recipes.md) and the [README](../README.md) don't already answer the question. Each section lists subcommands → flags → examples in the form the docs publish.

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

Mutating: `add`, `bind`, `unbind`, `delete`, `set-identity`. Confirm before running.

---

## `cron` — recurring + one-shot scheduled runs

Subcommands: `list`, `show <job-id>`, `add`, `edit <job-id>`, `run <job-id>`, `runs --id <job-id>`.

Schedule flags: `--at <datetime>`, `--tz <iana>`, `--keep-after-run`, `--cron "<expression>"`.
Session flags: `--session main|isolated|current|session:<id>`.
Delivery flags: `--announce`, `--no-deliver`, `--channel <name>`, `--to <target>`, `--thread-id <id>`, `--best-effort-deliver`, `--no-best-effort-deliver`.
Job flags: `--name <text>`, `--message <text>`, `--agent <id>`, `--clear-agent`, `--model <ref>`, `--light-context`.
Query flags: `--json`, `--limit <n>`, `--due`.

```bash
clawctl cli cron list --agent ops --json
clawctl cli cron show <job-id> --json
clawctl cli cron run <job-id> --due
clawctl cli cron runs --id <job-id> --limit 50
clawctl cli cron add --name "Brief" --cron "0 7 * * *" --session isolated --message "Summarize updates." --light-context
clawctl cli cron edit <job-id> --announce --channel telegram --to "123456789"
clawctl cli cron edit <job-id> --no-deliver
clawctl cli cron edit <job-id> --announce --channel slack --to "channel:C1234567890"
```

Mutating: `add`, `edit`, `run`. Confirm before running.

---

## `sessions` — stored conversation transcripts

Subcommands: `(default list)`, `export-trajectory`, `cleanup`.

Flags: `--agent <id>`, `--all-agents`, `--store <path>`, `--limit <n|all>`, `--verbose`, `--active <minutes>`, `--json`, `--session-key <key>`, `--workspace <path>`, `--output <directory>`, `--dry-run`, `--enforce`, `--fix-missing`, `--fix-dm-scope`, `--active-key <key>`.

```bash
clawctl cli sessions --all-agents --json --limit 25
clawctl cli sessions --active 120 --json
clawctl cli sessions export-trajectory --session-key "agent:main:telegram:direct:123" --workspace .
clawctl cli sessions cleanup --dry-run --all-agents
clawctl cli sessions cleanup --dry-run --fix-dm-scope
clawctl cli sessions cleanup --enforce --active-key "agent:main:telegram:direct:123"
```

Mutating: `cleanup --enforce`, `cleanup --fix-*`. Always preview with `--dry-run` first.

---

## `skills` — agent skills (search, install, update)

Subcommands: `search`, `install`, `update`, `list`, `info`, `check`.

Flags: `--limit <n>`, `--json`, `--version <version>`, `--force`, `--agent <id>`, `--all`, `--eligible`, `--verbose`.

```bash
clawctl cli skills search "calendar" --json --limit 20
clawctl cli skills install <slug> --version <version>
clawctl cli skills install <slug> --agent <id>
clawctl cli skills update --all --agent <id>
clawctl cli skills list --eligible --json
clawctl cli skills list --agent <id> --verbose
clawctl cli skills info <name> --agent <id> --json
clawctl cli skills check --agent <id> --json
```

Mutating: `install`, `update`. Confirm before running.

---

## `hooks` — lifecycle hooks

Subcommands: `list` (default), `info <name>`, `check`, `enable <name>`, `disable <name>`.

Flags: `--json`, `-v|--verbose`, `--yes`, `-l|--link`, `--pin`, `--all`, `--dry-run`, `--eligible`.

```bash
clawctl cli hooks list --json
clawctl cli hooks list --verbose
clawctl cli hooks info session-memory
clawctl cli hooks check --json
clawctl cli hooks enable session-memory
clawctl cli hooks disable command-logger
```

Hook packs are managed via `plugins install/update`, not `hooks install`.

---

## `secrets` — secret refs, audit, reload

Subcommands: `reload`, `audit`, `configure`, `apply`.

Flags: `--check`, `--json`, `--url <url>`, `--token <token>`, `--timeout <ms>`, `--allow-exec`, `--plan-out <path>`, `--apply`, `--yes`, `--providers-only`, `--skip-provider-setup`, `--agent <id>`, `--from <path>`, `--dry-run`.

```bash
clawctl cli secrets reload
clawctl cli secrets audit --check --json
clawctl cli secrets configure --plan-out /tmp/plan.json
clawctl cli secrets apply --from /tmp/plan.json --dry-run
clawctl cli secrets apply --from /tmp/plan.json --allow-exec
```

Mutating: `reload`, `apply`, `configure`. Always plan + dry-run before `apply`.

---

## `health` — gateway liveness (host CLI)

No subcommands. Flags: `--json`, `--timeout <ms>`, `--verbose`, `--debug`.

Prefer the wrapper's `clawctl health` (faster; talks HTTP not SSH). Use `clawctl cli health` only when you need host-side process info.

```bash
clawctl cli health --json
clawctl cli health --timeout 2500 --verbose
```

---

## `status` — readiness diagnostics

No subcommands. Flags: `--all`, `--deep`, `--usage`, `--json`.

```bash
clawctl cli status
clawctl cli status --usage --json
clawctl cli status --deep      # heavy: live probes across messaging providers
clawctl cli status --all --json
```

---

## `channels` — messaging channel accounts

Subcommands: `list`, `status`, `capabilities`, `resolve`, `logs`, `add`, `remove`, `login`, `logout`.

Flags: `--channel <name>`, `--account <id>`, `--json`, `--timeout <ms>`, `--probe`, `--target <dest>`, `--kind <auto|user|group>`, `--lines <n>`, `--token`, `--bot-token`, `--app-token`, `--private-key`, `--signal-number`, `--cli-path`, `--http-url`, `--homeserver`, `--user-id`, `--access-token`, `--ship`, `--url`, `--code`, `--use-env`, `--webhook-path`, `--webhook-url`, `--verbose`, `--delete`.

```bash
clawctl cli channels list --all --json
clawctl cli channels status --probe --json
clawctl cli channels capabilities --channel discord --target channel:123 --json
clawctl cli channels resolve --channel slack "#general" "@jane"
clawctl cli channels logs --channel all --lines 100
clawctl cli channels add --channel telegram --token <bot-token>
clawctl cli channels remove --channel telegram --delete
clawctl cli channels login --channel whatsapp --verbose
clawctl cli channels logout --channel whatsapp
```

Mutating: `add`, `remove`, `login`, `logout`. Channel auth is sensitive — confirm.

---

## `approvals` + `exec-policy` — command allowlist + auto-approve policy

Subcommands: `approvals get`, `approvals set`, `approvals allowlist add`, `approvals allowlist remove`, `exec-policy show`, `exec-policy preset <name>`, `exec-policy set`.

Flags: `--gateway`, `--node <id|name|ip>`, `--file <path>`, `--stdin`, `--json`, `--agent <id>` (default `*`), `--host <gateway|local>`, `--security <level>`, `--ask <on|off>`, `--ask-fallback <level>`, `--url`, `--token`, `--timeout`.

```bash
clawctl cli approvals get --json
clawctl cli approvals get --node <id|name|ip>
clawctl cli approvals get --gateway
clawctl cli exec-policy show --json
clawctl cli exec-policy preset cautious --json
clawctl cli exec-policy preset yolo
clawctl cli exec-policy set --host gateway --security full --ask off
clawctl cli approvals allowlist add "~/Projects/**/bin/rg"
clawctl cli approvals allowlist add --agent main --node <id|name|ip> "/usr/bin/uptime"
clawctl cli approvals allowlist remove "~/Projects/**/bin/rg"
```

Mutating: `set`, `preset`, `allowlist add|remove`. These widen the agent's blast radius — confirm explicitly.

---

## `models` — default + auth profiles

Main: `status`, `list`, `set <model-or-alias>`, `scan`. Aliases: `aliases list`, `fallbacks list`. Auth: `auth add`, `auth list`, `auth login --provider <id>`, `auth setup-token --provider <id>`, `auth paste-token`.

Flags: `--json`, `--plain`, `--check`, `--probe`, `--probe-provider <name>`, `--probe-profile <id>`, `--probe-timeout <ms>`, `--probe-concurrency <n>`, `--probe-max-tokens <n>`, `--agent <id>`, `--all`, `--provider <id>`, `--no-probe`, `--min-params <b>`, `--max-age-days <days>`, `--max-candidates <n>`, `--timeout <ms>`, `--concurrency <n>`, `--yes`, `--no-input`, `--set-default`, `--set-image`, `--profile-id`, `--expires-in <duration>`.

```bash
clawctl cli models status --probe --json
clawctl cli models list --provider openrouter --json
clawctl cli models set <model-or-alias>
clawctl cli models scan --max-candidates 30 --json --set-default
clawctl cli models auth list --provider openai-codex --json
clawctl cli models auth login --provider openai-codex --set-default
```

Prefer the wrapper's `clawctl models` for the cached registered-agent list. `clawctl cli models` is for provider config.

---

## `mcp` — MCP server definitions

Subcommands: `serve`, `list`, `show [name]`, `set <name> <json>`, `unset <name>`.

`serve` flags: `--url`, `--token`, `--token-file`, `--password`, `--password-file`, `--claude-channel-mode <auto|on|off>`, `-v|--verbose`.

```bash
clawctl cli mcp list
clawctl cli mcp show context7 --json
clawctl cli mcp set context7 '{"command":"uvx","args":["context7-mcp"]}'
clawctl cli mcp set docs '{"url":"https://mcp.example.com","transport":"streamable-http"}'
clawctl cli mcp unset context7
clawctl cli mcp serve --url ws://127.0.0.1:18789 --token-file ~/.openclaw/gateway.token
```

Mutating: `set`, `unset`. Confirm.

---

## `tasks` — background task tracking

Subcommands: `list`, `show`, `notify`, `cancel`, `audit`, `maintenance`, `flow list|show|cancel`.

Flags: `--json`, `--runtime <subagent|acp|cron|cli>`, `--status <queued|running|succeeded|failed|timed_out|cancelled|lost>`, `--severity <warn|error>`, `--code <name>`, `--limit <n>`, `--apply`.

```bash
clawctl cli tasks --json
clawctl cli tasks list --runtime acp --status running --json
clawctl cli tasks show <lookup> --json
clawctl cli tasks cancel <lookup>
clawctl cli tasks notify <lookup> state_changes
clawctl cli tasks audit --severity error --json
clawctl cli tasks maintenance              # preview
clawctl cli tasks maintenance --apply      # mutating; confirm
clawctl cli tasks flow list --json
clawctl cli tasks flow cancel <lookup>
```

---

## `plugins` — plugin lifecycle + ClawHub

Subcommands: `list`, `search`, `install`, `inspect`, `info`, `enable`, `disable`, `uninstall`, `update`, `registry`, `doctor`, `marketplace`.

Flags: `--enabled`, `--verbose`, `--json`, `--limit <n>`, `--force`, `--pin`, `--marketplace <name>`, `--dangerously-force-unsafe-install`, `-l|--link`, `--runtime`, `--all`, `--dry-run`, `--keep-files`, `--refresh`.

Install locators: `clawhub:<package>`, `npm:<package>`, `npm-pack:<path.tgz>`, `git:github.com/owner/repo[@<ref>]`, `<plugin>@<marketplace>`, `<path>`.

```bash
clawctl cli plugins list --enabled --json
clawctl cli plugins search foo --limit 20 --json
clawctl cli plugins install clawhub:@openclaw/some-plugin --pin
clawctl cli plugins install ./local-plugin -l
clawctl cli plugins inspect <name> --runtime --json
clawctl cli plugins update --all --dry-run
clawctl cli plugins uninstall <name> --dry-run
clawctl cli plugins registry --refresh
clawctl cli plugins doctor
```

Mutating: `install`, `enable`, `disable`, `uninstall`, `update`, `registry --refresh`. Never use `--dangerously-force-unsafe-install` without explicit user direction.

---

## `dns` — CoreDNS planning helper (macOS)

Subcommands: `setup`. Flags: `--domain <domain>`, `--apply` (sudo; macOS only).

```bash
clawctl cli dns setup
clawctl cli dns setup --domain openclaw.internal
clawctl cli dns setup --apply       # sudo + restarts CoreDNS; confirm
```

---

## `gateway` — gateway process + RPC

Run: `gateway` (alias `gateway run`).
Query: `gateway health`, `gateway usage-cost`, `gateway stability`, `gateway diagnostics export`, `gateway status`, `gateway probe`, `gateway call <method>`.
Service: `gateway install`, `gateway start`, `gateway stop`, `gateway restart`, `gateway uninstall`. Discovery: `gateway discover`.

Run flags: `--port`, `--bind <loopback|lan|tailnet|auto|custom>`, `--auth <token|password>`, `--token`, `--password`, `--password-file`, `--tailscale <off|serve|funnel>`, `--tailscale-reset-on-exit`, `--allow-unconfigured`, `--dev`, `--reset`, `--force`, `--verbose`, `--cli-backend-logs`, `--ws-log <auto|full|compact>`, `--compact`, `--raw-stream`, `--raw-stream-path <path>`.

Query flags (shared): `--url`, `--token`, `--password`, `--timeout`, `--expect-final`, `--json`, `--no-color`. Per-command: `usage-cost --days`; `stability --limit --type --since-seq --bundle --export --output`; `diagnostics export --output --log-lines --log-bytes --no-stability-bundle`; `status --no-probe --deep --require-rpc`; `probe --ssh --ssh-identity --ssh-auto`; `call --params <json>`.

Restart: `--safe`, `--force`, `--wait <duration>`. Install: `--port`, `--runtime <node|bun>`, `--token`, `--wrapper <path>`, `--force`. Discover: `--timeout <ms>`.

```bash
clawctl cli gateway health --json
clawctl cli gateway status --require-rpc --json
clawctl cli gateway usage-cost --days 7 --json
clawctl cli gateway stability --type payload.large --limit 50
clawctl cli gateway diagnostics export --output /tmp/openclaw-diag.zip
clawctl cli gateway probe --json
clawctl cli gateway call status
clawctl cli gateway call logs.tail --params '{"sinceMs": 60000}'
clawctl cli gateway discover --timeout 4000 --json
clawctl cli gateway restart --safe       # mutating
```

Mutating: anything in `Service` group, `restart`. These take the gateway down — always confirm.

---

## `doctor` — diagnostics + repair

No subcommands. Flags: `--no-workspace-suggestions`, `--yes`, `--repair|--fix`, `--force`, `--non-interactive`, `--generate-gateway-token`, `--deep`.

```bash
clawctl cli doctor                                    # read-only
clawctl cli doctor --deep
clawctl cli doctor --repair --non-interactive         # mutating; confirm
clawctl cli doctor --generate-gateway-token           # mutating; rotates the gateway token
```

`--force` overwrites custom service config — never run without explicit user direction.

---

## `logs` — gateway log stream

No subcommands. Flags: `--limit <n>` (default 200), `--max-bytes <n>` (default 250000), `--follow`, `--interval <ms>` (default 1000), `--json`, `--plain`, `--no-color`, `--local-time`, `--url`, `--token`, `--timeout`, `--expect-final`.

```bash
clawctl cli logs --limit 500 --json
clawctl cli logs --follow --interval 2000 --local-time
clawctl cli logs --plain --no-color
```

`--follow` streams — pipe through `head -n N` if you only want a sample.

---

## Conventions

- Every mutating call must be confirmed by the user before execution.
- Always prefer `--json | jq -r` over the human formatter when feeding output back into the conversation.
- When debugging a remote run, capture both `clawctl trace <id>` (Jaeger) and `clawctl cli logs --since-seq <n> --json` (gateway-side).
- Trace-ids and `traceparent` are the only safe identifiers to share across systems — never paste raw payloads.
